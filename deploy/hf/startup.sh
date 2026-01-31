#!/bin/bash
# MedBeads HF Space Startup Script
# This script initializes the database and ingests sample data before starting services

set -e

echo "========================================"
echo "MedBeads Startup Script"
echo "========================================"

# Start core server in background for data ingestion
echo "[1/5] Starting core server for initialization..."
cd /app/core
./medbeads-core &
CORE_PID=$!

# Wait for core to be ready
echo "[2/5] Waiting for core server to be ready..."
for i in {1..30}; do
    if curl -s http://127.0.0.1:8080/patients > /dev/null 2>&1; then
        echo "  Core server is ready!"
        break
    fi
    echo "  Waiting... ($i/30)"
    sleep 1
done

# Check if sample FHIR data needs to be ingested
echo "[3/5] Checking for sample FHIR data..."
if [ -d "/app/sample_data/fhir" ] && [ "$(ls -A /app/sample_data/fhir 2>/dev/null)" ]; then
    # Check if clearance test patients are already in database
    PATIENT_COUNT=$(curl -s http://127.0.0.1:8080/patients | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
    echo "  Current patient count: $PATIENT_COUNT"

    # If we have fewer than expected patients, ingest the sample data
    if [ "$PATIENT_COUNT" -lt "5" ]; then
        echo "  Ingesting sample FHIR data..."
        cd /app/scripts
        for f in /app/sample_data/fhir/*.json; do
            if [ -f "$f" ]; then
                echo "    Processing $(basename $f)..."
                python3 mass_ingest.py "$f" --limit 1 || echo "    Warning: Failed to ingest $(basename $f)"
            fi
        done
        echo "  Sample data ingestion complete."
    else
        echo "  Sample data already present, skipping ingestion."
    fi
else
    echo "  No sample FHIR data found, skipping ingestion."
fi

# Setup clearance rules if the script exists
echo "[4/5] Setting up security clearance rules..."
if [ -f "/app/sample_data/setup_clearance_rules.py" ]; then
    cd /app/sample_data
    python3 setup_clearance_rules.py --api-url http://127.0.0.1:8080 || echo "  Warning: Clearance setup failed or already exists"
else
    echo "  Clearance setup script not found, skipping."
fi

# Stop the temporary core server
echo "[5/5] Stopping temporary core server..."
kill $CORE_PID 2>/dev/null || true
sleep 2

echo "========================================"
echo "Initialization complete! Starting services..."
echo "========================================"

# Start supervisord with all services
exec /usr/bin/supervisord -c /etc/supervisor/conf.d/supervisord.conf
