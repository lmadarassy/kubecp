#!/bin/sh
# Inject OIDC issuer URL into the Angular app at container startup
# The OIDC_ISSUER env var is set by the Helm chart / deployment
ISSUER="${OIDC_ISSUER:-https://auth.example.com/realms/hosting}"
echo "Configuring OIDC issuer: ${ISSUER}"
# Replace the placeholder in all JS files
find /usr/share/nginx/html -name '*.js' -exec \
  sed -i "s|https://auth.example.com/realms/hosting|${ISSUER}|g" {} +
