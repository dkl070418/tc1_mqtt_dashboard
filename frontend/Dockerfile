# ------ 构建阶段（始终在宿主平台运行 Node，无需模拟）------
FROM --platform=$BUILDPLATFORM node:20-alpine AS builder
# 国内构建传 --build-arg NPM_REGISTRY=https://registry.npmmirror.com 加速
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /app
COPY frontend/package*.json ./
RUN npm ci --registry=${NPM_REGISTRY}
COPY frontend/ ./
RUN npm run build

# ------ 运行阶段 ------
FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx/nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
