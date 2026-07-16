# syntax=docker/dockerfile:1.7

FROM node:24-bookworm-slim AS builder

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci

COPY . .

ARG VITE_API_BASE_URL=/api/core
ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}

RUN npm run build

FROM nginx:1.28-alpine

LABEL org.opencontainers.image.title="MedBeads paper-demo UI" \
      org.opencontainers.image.description="Synthetic-data visualizer; not for clinical use" \
      org.opencontainers.image.source="https://github.com/medbeads/medbeads" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.medbeads.distribution-profile="public-paper-demo"

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 5174

HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=5 \
  CMD wget -q -O /dev/null http://127.0.0.1:5174/healthz || exit 1

CMD ["nginx", "-g", "daemon off;"]
