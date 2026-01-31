#!/usr/bin/env python3
"""
Ingest sample patient data into MedBeads for Security Clearance testing.

Usage:
    python ingest_sample_patients.py [--with-clearance]

Options:
    --with-clearance    Also create clearance rules based on scenarios
"""

import json
import requests
import sys
from pathlib import Path

API_BASE = "http://localhost:8080"

def ingest_bead(bead: dict, parent_ids: list[str] = None) -> str:
    """Ingest a single bead and return its ID."""
    payload = {
        "type": bead["type"],
        "timestamp": bead["timestamp"],
        "parents": parent_ids or bead.get("parents", []),
        "content": bead["content"]
    }

    response = requests.post(f"{API_BASE}/beads", json=payload)
    if response.status_code != 200:
        raise Exception(f"Failed to ingest bead: {response.text}")

    result = response.json()
    return result["id"]


def create_clearance_rule(bead_id: str, denied_roles: list[str], reason: str, expires_at: str = None):
    """Create a clearance rule for a bead."""
    payload = {
        "bead_id": bead_id,
        "denied_roles": denied_roles,
        "reason": reason
    }
    if expires_at:
        payload["expires_at"] = expires_at

    response = requests.post(f"{API_BASE}/clearance", json=payload)
    if response.status_code not in [200, 201]:
        print(f"  Warning: Failed to create clearance rule: {response.text}")
    else:
        print(f"  Created clearance rule: denied={denied_roles}")


def ingest_patient(patient: dict, with_clearance: bool = False):
    """Ingest a patient and all their beads."""
    print(f"\nIngesting patient: {patient['id']} - {patient['scenario']}")

    beads = patient["beads"]
    bead_ids = {}  # type -> id mapping
    bead_ids_list = []  # ordered list for clearance matching

    # First bead should be patient_registration (root)
    root_bead = beads[0]
    if root_bead["type"] != "patient_registration":
        raise Exception("First bead must be patient_registration")

    root_id = ingest_bead(root_bead)
    bead_ids["patient_registration"] = root_id
    bead_ids_list.append({"type": root_bead["type"], "id": root_id})
    print(f"  Root: {root_id[:16]}... ({root_bead['type']})")

    # Ingest child beads with root as parent
    for bead in beads[1:]:
        bead_id = ingest_bead(bead, [root_id])
        bead_type = bead["type"]

        # Store in mapping (may overwrite if multiple of same type)
        if bead_type not in bead_ids:
            bead_ids[bead_type] = bead_id

        bead_ids_list.append({"type": bead_type, "id": bead_id})
        print(f"  Child: {bead_id[:16]}... ({bead_type})")

    # Create clearance rules if requested
    if with_clearance and patient.get("clearance_examples"):
        print("  Creating clearance rules...")
        for rule in patient["clearance_examples"]:
            bead_type = rule["bead_type"]
            denied_roles = rule["denied_roles"]
            reason = rule.get("reason", "")
            expires_at = rule.get("expires_at")
            bead_index = rule.get("bead_index")

            # Find the bead ID
            if bead_index is not None:
                # Find nth bead of this type
                matching = [b for b in bead_ids_list if b["type"] == bead_type]
                if bead_index < len(matching):
                    target_id = matching[bead_index]["id"]
                else:
                    print(f"    Warning: bead_index {bead_index} not found for type {bead_type}")
                    continue
            elif bead_type in bead_ids:
                target_id = bead_ids[bead_type]
            else:
                print(f"    Warning: No bead of type {bead_type} found")
                continue

            create_clearance_rule(target_id, denied_roles, reason, expires_at)

    return root_id


def main():
    with_clearance = "--with-clearance" in sys.argv

    # Load sample data
    script_dir = Path(__file__).parent
    data_file = script_dir / "sample_patients.json"

    if not data_file.exists():
        print(f"Error: {data_file} not found")
        sys.exit(1)

    with open(data_file) as f:
        data = json.load(f)

    print("=" * 60)
    print("MedBeads Sample Patient Ingestion")
    print("=" * 60)

    if with_clearance:
        print("Mode: With Clearance Rules")
    else:
        print("Mode: Data Only (use --with-clearance to add restrictions)")

    # Check API is reachable
    try:
        response = requests.get(f"{API_BASE}/patients", timeout=5)
    except requests.exceptions.ConnectionError:
        print(f"\nError: Cannot connect to API at {API_BASE}")
        print("Please ensure the MedBeads server is running.")
        sys.exit(1)

    # Ingest each patient
    patient_ids = []
    for patient in data["patients"]:
        try:
            patient_id = ingest_patient(patient, with_clearance)
            patient_ids.append({
                "id": patient["id"],
                "bead_id": patient_id,
                "scenario": patient["scenario"]
            })
        except Exception as e:
            print(f"  Error: {e}")

    # Summary
    print("\n" + "=" * 60)
    print("Summary")
    print("=" * 60)
    for p in patient_ids:
        print(f"  {p['id']}: {p['bead_id'][:16]}...")
        print(f"    Scenario: {p['scenario']}")

    print(f"\nTotal: {len(patient_ids)} patients ingested")


if __name__ == "__main__":
    main()
