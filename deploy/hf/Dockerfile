# ------------------------------------
# Stage 1: Build Core (Go)
# ------------------------------------
FROM golang:1.23-alpine AS build-core
WORKDIR /app/core
COPY core/go.mod core/go.sum ./
RUN go mod download
COPY core/ ./
RUN go build -o medbeads-core main.go

# ------------------------------------
# Stage 2: Build UI (Node)
# ------------------------------------
FROM node:20-alpine AS build-ui
WORKDIR /app/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm install
COPY ui/ ./
# Build for relative path usage, assuming Nginx handles routing
# We must ensure VITE_API_BASE_URL is set for the build
# Since Nginx will serve Core at /api/core and AI at /api/ai
ENV VITE_API_BASE_URL=/api/core
ENV VITE_AI_API_BASE_URL=/api/ai
RUN npm run build

# ------------------------------------
# Stage 3: Runtime (Python + Nginx + Supervisord)
# ------------------------------------
# Using Python slim as base
FROM python:3.11-slim

# Install system dependencies (Nginx, Supervisor, generic libs)
RUN apt-get update && apt-get install -y \
    nginx \
    supervisor \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 1. Setup API (Python)
COPY api/ /app/api/
RUN pip install --no-cache-dir -r /app/api/requirements.txt || pip install fastapi uvicorn requests python-dotenv google-generativeai

# 2. Setup Core (Go)
COPY --from=build-core /app/core/medbeads-core /app/core/medbeads-core
RUN mkdir -p /app/core/medbeads_data

# 3. Setup UI (Static files)
COPY --from=build-ui /app/ui/dist /app/ui/dist

# 4. Configs
COPY deploy/hf/nginx.conf /etc/nginx/nginx.conf
COPY deploy/hf/supervisord.conf /etc/supervisor/conf.d/supervisord.conf

# 5. Sample Data (Pre-populated Bead objects + FHIR source files for ingestion)
# For HF Space demo, we include both pre-existing objects and new FHIR data
COPY sample_data/objects /app/core/medbeads_data/objects
COPY sample_data/fhir /app/sample_data/fhir
COPY sample_data/setup_clearance_rules.py /app/sample_data/

# 6. Ingestion scripts (for processing FHIR data)
COPY scripts/mass_ingest.py /app/scripts/

# 7. Startup script
COPY deploy/hf/startup.sh /app/startup.sh
RUN chmod +x /app/startup.sh

# Permissions (HF Spaces runs as user 1000 usually, but root is fine for initial setup)
# Ensure data dir is writable
RUN chmod -R 777 /app/core/medbeads_data

# Hugging Face Spaces exposes port 7860
EXPOSE 7860

# Start with initialization script (ingests data, then starts supervisor)
CMD ["/app/startup.sh"]
