# Multi-stage build for the file-space web client.
# Builds the Vite production bundle, then serves it as static files via
# nginx. VITE_API_URL is left empty at build time (see docker-compose.yml)
# so the app calls the API same-origin (/api/...) — cloudflared routes
# /api/* to the api service and everything else here. See
# REQUIREMENTS.md §5.6 and deploy/cloudflared/config.yml.example.

FROM node:26-alpine AS builder
WORKDIR /src

COPY package.json package-lock.json ./
RUN npm ci

COPY . .
ARG VITE_API_URL=""
ENV VITE_API_URL=$VITE_API_URL
RUN npm run build

FROM nginx:1.27-alpine
COPY --from=builder /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80
