#!/bin/bash
# Initialize sample data for docker-compose
# This script ingests FHIR data and sets up clearance rules

set -e

echo "========================================"
echo "MedBeads Sample Data Initialization"
echo "========================================"

# Wait for core to be ready
echo "[1/3] Waiting for core server..."
for i in {1..30}; do
    if curl -s http://localhost:8080/patients > /dev/null 2>&1; then
        echo "  Core server is ready!"
        break
    fi
    echo "  Waiting... ($i/30)"
    sleep 1
done

# Check patient count
PATIENT_COUNT=$(curl -s http://localhost:8080/patients | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
echo "  Current patient count: $PATIENT_COUNT"

# Ingest sample data if needed
if [ "$PATIENT_COUNT" -lt "5" ]; then
    echo "[2/3] Ingesting sample FHIR data..."

    if [ -d "sample_data/fhir" ]; then
        for f in sample_data/fhir/*.json; do
            if [ -f "$f" ]; then
                echo "    Processing $(basename $f)..."
                python3 scripts/mass_ingest.py "$f" --limit 1 || echo "    Warning: Failed to ingest $(basename $f)"
            fi
        done
    else
        echo "  sample_data/fhir not found, skipping..."
    fi
else
    echo "[2/3] Sample data already present, skipping ingestion."
fi

# Setup clearance rules
echo "[3/3] Setting up security clearance rules..."
if [ -f "sample_data/setup_clearance_rules.py" ]; then
    python3 sample_data/setup_clearance_rules.py --api-url http://localhost:8080 || echo "  Warning: Clearance setup failed or already exists"
else
    echo "  Clearance setup script not found, skipping."
fi

echo "========================================"
echo "Initialization complete!"
echo "========================================"
