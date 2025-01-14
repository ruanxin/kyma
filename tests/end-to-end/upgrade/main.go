package main

import (
	"flag"
	"fmt"
	"time"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/client/versioned/typed/monitoring/v1"
	sc "github.com/kubernetes-incubator/service-catalog/pkg/client/clientset_generated/clientset"
	mfClient "github.com/kyma-project/kyma/common/microfrontend-client/pkg/client/clientset/versioned"
	ab "github.com/kyma-project/kyma/components/application-broker/pkg/client/clientset/versioned"
	ao "github.com/kyma-project/kyma/components/application-operator/pkg/client/clientset/versioned"
	"github.com/kyma-project/kyma/components/kyma-operator/pkg/overrides"
	bu "github.com/kyma-project/kyma/components/service-binding-usage-controller/pkg/client/clientset/versioned"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/vrischmann/envconfig"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	k8sclientset "k8s.io/client-go/kubernetes"
	restClient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/internal/platform/logger"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/internal/platform/signal"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/internal/runner"
	dex "github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/fetch-dex-token"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/injector"
	apigateway "github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/api-gateway"
	applicationoperator "github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/application-operator"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/eventing"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/logging"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/monitoring"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/rafter"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/serverless"
	servicecatalog "github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/service-catalog"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/tests/ui"
	"github.com/kyma-project/kyma/tests/end-to-end/upgrade/pkg/waiter"
)

// Config holds application configuration
type Config struct {
	Logger                 logger.Config
	DexUserSecret          string `envconfig:"default=admin-user"`
	DexNamespace           string `envconfig:"default=kyma-system"`
	KubeNamespace          string `envconfig:"default=kube-system"`
	MaxConcurrencyLevel    int    `envconfig:"default=1"`
	KubeConfigPath         string `envconfig:"optional"`
	TestingAddonsURL       string
	WorkingNamespace       string `envconfig:"default=e2e-upgrade-test"`
	TestsInfoConfigMapName string `envconfig:"default=upgrade-tests-info"`
	EventSubscriberImage   string
}

const (
	prepareDataActionName  = "prepareData"
	executeTestsActionName = "executeTests"
)

func main() {
	actionUsage := fmt.Sprintf("Define what kind of action runner should execute. Possible values: %s or %s", prepareDataActionName, executeTestsActionName)

	var action string
	var verbose bool
	var testName string
	flag.StringVar(&action, "action", "", actionUsage)
	flag.StringVar(&testName, "testName", "", "limit test to test with name")
	flag.BoolVar(&verbose, "verbose", false, "Print all test logs")
	flag.Parse()

	var cfg Config
	err := envconfig.InitWithPrefix(&cfg, "APP")
	fatalOnError(err, "while reading configuration from environment variables")

	log := logger.New(&cfg.Logger)

	// Set up signals so we can handle the first shutdown signal gracefully
	stopCh := signal.SetupChannel()

	// K8s client
	k8sConfig, err := newRestClientConfig(cfg.KubeConfigPath)
	fatalOnError(err, "while creating k8s client cfg")

	k8sCli, err := k8sclientset.NewForConfig(k8sConfig)
	fatalOnError(err, "while creating k8s clientset")

	scCli, err := sc.NewForConfig(k8sConfig)
	fatalOnError(err, "while creating Service Catalog clientset")

	buCli, err := bu.NewForConfig(k8sConfig)
	fatalOnError(err, "while creating Binding Usage clientset")

	appConnectorCli, err := ao.NewForConfig(k8sConfig)
	fatalOnError(err, "while creating Application Connector clientset")

	appBrokerCli, err := ab.NewForConfig(k8sConfig)
	fatalOnError(err, "while creating Application Broker clientset")

	domainName, err := getDomainNameFromCluster(k8sCli)
	fatalOnError(err, "while reading domain name from cluster")

	mfCli, err := mfClient.NewForConfig(k8sConfig)
	fatalOnError(err, "while creating Microfrontends clientset")

	dynamicCli, err := dynamic.NewForConfig(k8sConfig)
	fatalOnError(err, "while creating K8s Dynamic client")

	dexConfig, err := getDexConfigFromCluster(k8sCli, cfg.DexUserSecret, cfg.DexNamespace, domainName)
	fatalOnError(err, "while reading dex config from cluster")

	monitoringCli, err := monitoringv1.NewForConfig(k8sConfig)
	fatalOnError(err, "while generating monitoring client")

	// Register tests. Convention:
	// <test-name> : <test-instance>

	// Using map is on purpose - we ensure that test name will not be duplicated.
	// Test name is sanitized and used for creating dedicated namespace for given test,
	// so it cannot overlap with others.

	metricUpgradeTest, err := monitoring.NewMetricsUpgradeTest(k8sCli)
	fatalOnError(err, "while creating Metrics Upgrade Test")

	aInjector, err := injector.NewAddons("end-to-end-upgrade-test", cfg.TestingAddonsURL)
	fatalOnError(err, "while creating addons configuration injector")

	tests := map[string]runner.UpgradeTest{
		"HelmBrokerUpgradeTest":           servicecatalog.NewHelmBrokerTest(aInjector, k8sCli, scCli, buCli),
		"HelmBrokerConflictUpgradeTest":   servicecatalog.NewHelmBrokerConflictTest(aInjector, k8sCli, scCli, buCli),
		"ApplicationBrokerUpgradeTest":    servicecatalog.NewAppBrokerUpgradeTest(scCli, k8sCli, buCli, appBrokerCli, appConnectorCli),
		"GrafanaUpgradeTest":              monitoring.NewGrafanaUpgradeTest(k8sCli),
		"TargetsAndRulesUpgradeTest":      monitoring.NewTargetsAndRulesTest(k8sCli, monitoringCli),
		"MetricsUpgradeTest":              metricUpgradeTest,
		"MicrofrontendUpgradeTest":        ui.NewMicrofrontendUpgradeTest(mfCli),
		"ClusterMicrofrontendUpgradeTest": ui.NewClusterMicrofrontendUpgradeTest(mfCli),
		"ApiGatewayUpgradeTest":           apigateway.NewApiGatewayTest(k8sCli, dynamicCli, domainName, dexConfig.IdProviderConfig()),
		"ApplicationOperatorUpgradeTest":  applicationoperator.NewApplicationOperatorUpgradeTest(appConnectorCli, *k8sCli),
		"RafterUpgradeTest":               rafter.NewRafterUpgradeTest(dynamicCli),
		"EventingUpgradeTest":             eventing.NewEventingUpgradeTest(k8sCli, appConnectorCli, appBrokerCli, scCli, dynamicCli, cfg.EventSubscriberImage),
		"ServerlessUpgradeTest":           serverless.New(dynamicCli),
		"LoggingUpgradeTest":              logging.NewLoggingTest(k8sCli, domainName, dexConfig.IdProviderConfig()),
	}

	if testToRun, found := tests[testName]; found {
		tests = map[string]runner.UpgradeTest{testName: testToRun}
	}

	tRegistry, err := runner.NewConfigMapTestRegistry(k8sCli, cfg.WorkingNamespace, cfg.TestsInfoConfigMapName)
	fatalOnError(err, "while creating Test Registry")

	// Execute requested action
	testRunner, err := runner.NewTestRunner(log, k8sCli.CoreV1().Namespaces(), tRegistry, tests, cfg.MaxConcurrencyLevel, verbose)
	fatalOnError(err, "while creating test runner")

	switch action {
	case prepareDataActionName:
		err := testRunner.PrepareData(stopCh)
		fatalOnError(err, "while executing prepare data for all registered tests")
	case executeTestsActionName:
		err := testRunner.ExecuteTests(stopCh)
		fatalOnError(err, "while executing tests for all registered tests")
	default:
		logrus.Fatalf("Unrecognized runner action. Allowed actions: %s or %s.", prepareDataActionName, executeTestsActionName)
	}
}

func fatalOnError(err error, context string) {
	if err != nil {
		logrus.Fatal(fmt.Sprintf("%s: %v", context, err))
	}
}

func newRestClientConfig(kubeConfigPath string) (*restClient.Config, error) {
	if kubeConfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	}

	return restClient.InClusterConfig()
}

func getDomainNameFromCluster(k8sCli *k8sclientset.Clientset) (string, error) {
	overridesData := overrides.New(k8sCli)

	coreOverridesYaml, err := overridesData.ForRelease("core")
	if err != nil {
		return "", err
	}

	coreOverridesMap, err := overrides.ToMap(coreOverridesYaml)
	if err != nil {
		return "", err
	}

	value, found := overrides.FindOverrideStringValue(coreOverridesMap, "global.domainName")
	logrus.Infof("using domainName: %v", value)

	if !found || value == "" {
		return "", errors.New("Could not get valid domain name")
	}
	return value, nil
}

func getDexConfigFromCluster(k8sCli *k8sclientset.Clientset, userSecret, dexNamespace, domainName string) (dex.Config, error) {
	dexConfig := dex.Config{}
	err := waiter.WaitAtMost(func() (done bool, err error) {
		secret, err := k8sCli.CoreV1().Secrets(dexNamespace).Get(userSecret, metav1.GetOptions{})
		if err != nil {
			logrus.Infof("while getting dex secret: %v", err)
			return false, nil
		}
		dexConfig = dex.Config{
			Domain:       domainName,
			UserEmail:    string(secret.Data["email"]),
			UserPassword: string(secret.Data["password"]),
		}
		return true, nil
	}, time.Second*30, nil)
	if err != nil {
		return dex.Config{}, errors.Wrapf(err, "while waiting for dex config secret %s", userSecret)
	}
	return dexConfig, nil
}
