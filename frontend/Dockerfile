
 FROM node:22-alpine AS build

 WORKDIR /app

 COPY package.json ./package.json
 RUN npm install --no-fund --no-audit && npm cache clean --force

 COPY index.html ./index.html
 COPY vite.config.js ./vite.config.js
 COPY src ./src

 RUN npm run build

FROM nginx:1.27-alpine-slim

COPY nginx-main.conf /etc/nginx/nginx.conf
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 3000

CMD ["nginx", "-g", "daemon off;"]
