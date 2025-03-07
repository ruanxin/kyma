const axios = require("axios");
const fs = require("fs");
const { debug, getEnvOrThrow } = require("../utils");
const { OAuthCredentials, OAuthToken } = require("../lib/oauth");

const SCOPES = ["broker:write", "cld:read"];
const KYMA_SERVICE_ID = "47c9dcbf-ff30-448e-ab36-d3bad66ba281";

class KEBConfig {
  static fromEnv() {
    return new KEBConfig(
      getEnvOrThrow("KEB_HOST"),
      OAuthCredentials.fromEnv("KEB_CLIENT_ID", "KEB_CLIENT_SECRET"),
      getEnvOrThrow("KEB_GLOBALACCOUNT_ID"),
      getEnvOrThrow("KEB_SUBACCOUNT_ID"),
      getEnvOrThrow("KEB_USER_ID"),
      getEnvOrThrow("KEB_PLAN_ID"),
      process.env.KEB_REGION
    );
  }

  constructor(host, credentials, globalAccountID, subaccountID, userID, planID, region) {
    this.host = host;
    this.credentials = credentials;
    this.globalAccountID = globalAccountID;
    this.subaccountID = subaccountID;
    this.userID = userID;
    this.planID = planID;
    this.region = region;
  }
}

class KEBClient {
  constructor(config) {
    this.token = new OAuthToken(`https://oauth2.${config.host}/oauth2/token`, config.credentials);
    this.host = config.host;
    this.globalAccountID = config.globalAccountID;
    this.subaccountID = config.subaccountID;
    this.userID = config.userID;
    this.planID = config.planID;
    this.region = config.region;
  }

  async buildRequest(payload, endpoint, verb) {
    const token = await this.token.getToken(SCOPES);
    const region = this.getRegion();
    const url = `https://kyma-env-broker.${this.host}/oauth/${region}v2/${endpoint}`;
    const headers = {
      "X-Broker-API-Version": 2.14,
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    };

    const request = {
      url: url,
      method: verb,
      headers: headers,
      data: payload,
    };
    return request;
  }

  async callKEB(payload, endpoint, verb) {
    const config = await this.buildRequest(payload, endpoint, verb);

    try {
      const resp = await axios.request(config);
      if (resp.data.errors) {
        debug(resp);
        throw new Error(resp.data);
      }
      return resp.data;
    } catch (err) {
      debug(err);
      const msg = "Error calling KEB";
      if (err.response) {
        throw new Error(`${msg}: ${err.response.status} ${err.response.statusText}`);
      } else {
        throw new Error(`${msg}: ${err.toString()}`);
      }
    }
  }

  async getSKR(instanceID) {
    const endpoint = `service_instances/${instanceID}`;
    try {
      return await this.callKEB({}, endpoint, "get");
    } catch (err) {
      throw new Error(`error while getting SKR: ${err.toString()}`);
    }
  }

  async provisionSKR(name, instanceID, platformCreds, btpOperatorCreds, customParams) {
    const payload = {
      service_id: KYMA_SERVICE_ID,
      plan_id: this.planID,
      context: {
        globalaccount_id: this.globalAccountID,
        subaccount_id: this.subaccountID,
        user_id: this.userID,
      },
      parameters: {
        name: name,
        ...customParams,
      },
    };

    if (platformCreds && btpOperatorCreds) {
      payload.context["sm_platform_credentials"] = {
        credentials: {
          basic: {
            username: platformCreds.credentials.username,
            password: platformCreds.credentials.password,
          },
        },
        url: btpOperatorCreds.smURL,
      };
    }

    const endpoint = `service_instances/${instanceID}`;
    try {
      return await this.callKEB(payload, endpoint, "put");
    } catch (err) {
      throw new Error(`error while provisioning SKR: ${err.toString()}`);
    }
  }

  async updateSKR(instanceID, customParams) {
    const payload = {
      service_id: KYMA_SERVICE_ID,
      context: {
        globalaccount_id: this.globalAccountID,
      },
      parameters: {
        ...customParams,
      },
    };
    const endpoint = `service_instances/${instanceID}?accepts_incomplete=true`;
    try {
      return await this.callKEB(payload, endpoint, "patch");
    } catch (err) {
      throw new Error(`error while updating SKR: ${err.toString()}`);
    }
  }

  async getOperation(instanceID, operationID) {
    const endpoint = `service_instances/${instanceID}/last_operation?operation=${operationID}`;
    try {
      return await this.callKEB({}, endpoint, "get");
    } catch (err) {
      debug(err.toString());
      return new Error(`error while checking SKR State: ${err.toString()}`);
    }
  }

  async deprovisionSKR(instanceID) {
    const endpoint = `service_instances/${instanceID}?service_id=${KYMA_SERVICE_ID}&plan_id=${this.planID}`;
    try {
      return await this.callKEB(null, endpoint, "delete");
    } catch (err) {
      return new Error(`error while deprovisioning SKR: ${err.toString()}`);
    }
  }

  async downloadKubeconfig(instanceID) {
    return new Promise(async (resolve, reject) => {
      let writeStream = fs
        .createWriteStream("./shoot-kubeconfig.yaml")
        .on("error", function (err) {
          reject(err);
        })
        .on("finish", function () {
          writeStream.close();
          fs.readFile("./shoot-kubeconfig.yaml", "utf8", (err, data) => {
            fs.unlinkSync("./shoot-kubeconfig.yaml");
            resolve(data);
          });
        });

      try {
        const resp = await axios.request({
          method: "get",
          url: `https://kyma-env-broker.${this.host}/kubeconfig/${instanceID}`,
          responseType: "stream",
        });
        if (resp.data.errors) {
          debug(resp);
          throw new Error(resp.data);
        }
        resp.data.pipe(writeStream);
      } catch (err) {
        debug(err);
        fs.unlinkSync("./shoot-kubeconfig.yaml");
        reject(err);
      }
    });
  }

  getRegion() {
    if (this.region && this.region != "") {
      return `${this.region}/`;
    }
    return "";
  }
}

module.exports = {
  KEBConfig,
  KEBClient,
};
