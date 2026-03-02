FROM node:20 AS builder
WORKDIR /app
COPY package.json ./
RUN npm install
COPY . .
RUN npx ng build --configuration=production

FROM nginx:alpine
COPY --from=builder /app/dist/panel-ui/browser /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY docker-entrypoint.sh /docker-entrypoint.d/40-configure-oidc.sh
RUN chmod +x /docker-entrypoint.d/40-configure-oidc.sh
EXPOSE 80
