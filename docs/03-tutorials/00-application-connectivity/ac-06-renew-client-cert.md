---
title: Renew a client certificate
---

By default, a client certificate you generate when you connect an external solution to Kyma is valid for 92 days. Follow this tutorial to renew a client certificate.
 
>**NOTE:** You, as the user, are responsible for rotating and renewing the certificates.

>**NOTE:** You can only renew client certificates that are still valid. If your client certificate is expired or revoked, you must generate a new one.

1. To renew a client certificate, use the certificate subject that matches the subject of your current certificate. To check the certificate subject, run:

   ```bash
   openssl x509 -noout -subject -in {PATH_TO_OLD_CLIENT_CERT}
   ```

2. Generate a new Certificate Signing Request (CSR) using the certificate subject you got in the previous step.

   ```bash
   openssl req -new -sha256 -out renewal.csr -key {PATH_TO_KEY} -subj "{SUBJECT}"
   ```

3. Send a request to Connector Service to renew the certificate. Use the certificate renewal endpoint.

   >**NOTE:** To get the URL to the certificate renewal endpoint, see the tutorial on how to [call the metadata endpoint](../../03-tutorials/00-application-connectivity/ac-02-get-client-certificate.md#call-the-metadata-endpoint).

   ```bash
   curl -X POST https://gateway.{CLUSTER_DOMAIN}/v1/applications/certificates/renewals -d '{"csr":"BASE64_ENCODED_CSR"}' --cert {PATH_TO_OLD_CLIENT_CERT} --key {PATH_TO_KEY}
   ```

   > **CAUTION:** On a local Kyma deployment, skip SSL certificate verification when making a `curl` call, by adding the `-k` flag to it. Alternatively, add the Kyma certificates to your local certificate storage on your machine using the `kyma import certs` command.

   A successful call returns a renewed client certificate:

   ```json
   {
       "crt":"BASE64_ENCODED_CRT_CHAIN",
       "clientCrt":"BASE64_ENCODED_CLIENT_CERT",
       "caCrt":"BASE64_ENCODED_CA_CERT"
   }
   ```