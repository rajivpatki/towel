FROM node:22-alpine AS build

LABEL org.opencontainers.image.source="https://github.com/rajivpatki/YOUR_REPO"

WORKDIR /app

COPY package.json ./package.json
COPY package-lock.json ./package-lock.json
RUN npm ci --no-fund --no-audit && npm cache clean --force

COPY public ./public
COPY index.html ./index.html
COPY vite.config.js ./vite.config.js
COPY src ./src

RUN npm run build

FROM nginx:1.27-alpine-slim

LABEL org.opencontainers.image.source="https://github.com/rajivpatki/YOUR_REPO"

COPY nginx-main.conf /etc/nginx/nginx.conf
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 3000

CMD ["nginx", "-g", "daemon off;"]
