#!/usr/bin/env python3
"""
Setup sample clearance rules for security clearance testing.

This script should be run AFTER the FHIR data has been ingested into MedBeads.
It creates clearance rules for specific beads to demonstrate the security clearance feature.

Usage:
    python setup_clearance_rules.py [--api-url http://localhost:8080]
"""

import requests
import json
import argparse
import os
from datetime import datetime, timedelta

API_BASE = os.environ.get("CORE_URL", "http://localhost:8080")

# Sample clearance rules to create
# These will be matched to beads by searching for specific content
CLEARANCE_SCENARIOS = [
    {
        "patient_name": "Tanaka Yuki",
        "search_terms": ["Metrorrhagia", "Irregular uterine bleeding"],
        "denied_roles": ["family"],
        "reason": "Patient privacy request - gynecological information",
        "expires_at": None
    },
    {
        "patient_name": "Tanaka Yuki",
        "search_terms": ["pregnancy test", "Urine pregnancy"],
        "denied_roles": ["family"],
        "reason": "Sensitive reproductive health information",
        "expires_at": None
    },
    {
        "patient_name": "Yamamoto Taro",
        "search_terms": ["Suspected prostate cancer", "Neoplasm of prostate"],
        "denied_roles": ["patient", "family"],
        "reason": "Awaiting patient counseling session before disclosure",
        "expires_at": (datetime.now() + timedelta(days=14)).isoformat()
    },
    {
        "patient_name": "Yamamoto Taro",
        "search_terms": ["PSA", "Prostate Specific Antigen"],
        "denied_roles": ["patient", "family"],
        "reason": "Lab results pending physician review and patient notification",
        "expires_at": (datetime.now() + timedelta(days=14)).isoformat()
    },
    {
        "patient_name": "Suzuki Kenji",
        "search_terms": ["Depressive disorder", "Major depressive"],
        "denied_roles": ["insurance"],
        "reason": "Patient explicit request - mental health privacy",
        "expires_at": None
    },
    {
        "patient_name": "Suzuki Kenji",
        "search_terms": ["Anxiety disorder", "Generalized anxiety"],
        "denied_roles": ["insurance"],
        "reason": "Mental health condition - restricted disclosure",
        "expires_at": None
    },
    {
        "patient_name": "Suzuki Kenji",
        "search_terms": ["Escitalopram", "Alprazolam"],
        "denied_roles": ["insurance"],
        "reason": "Psychiatric medication - restricted",
        "expires_at": None
    },
    {
        "patient_name": "Suzuki Kenji",
        "search_terms": ["Psychiatry progress note"],
        "denied_roles": ["insurance", "family"],
        "reason": "Confidential mental health documentation",
        "expires_at": None
    },
    {
        "patient_name": "Nakamura Ryo",
        "search_terms": ["Alcohol intoxication", "Acute alcohol"],
        "denied_roles": ["family", "insurance"],
        "reason": "Patient request - may affect employment",
        "expires_at": None
    },
    {
        "patient_name": "Nakamura Ryo",
        "search_terms": ["Blood alcohol level"],
        "denied_roles": ["family", "insurance"],
        "reason": "Sensitive substance-related information",
        "expires_at": None
    },
    {
        "patient_name": "Nakamura Ryo",
        "search_terms": ["THC", "cannabis", "Drug screen"],
        "denied_roles": ["family", "insurance", "specialist", "nurse"],
        "reason": "Highly sensitive - legal implications - primary care only",
        "expires_at": None
    },
    {
        "patient_name": "Nakamura Ryo",
        "search_terms": ["Social work assessment"],
        "denied_roles": ["family", "insurance", "specialist", "nurse"],
        "reason": "Confidential social work documentation",
        "expires_at": None
    }
]


def search_beads(query: str) -> list:
    """Search for beads containing the query text."""
    try:
        response = requests.get(f"{API_BASE}/search", params={"q": query})
        if response.status_code == 200:
            return response.json()
    except Exception as e:
        print(f"  Error searching for '{query}': {e}")
    return []


def get_patient_timeline(patient_id: str) -> list:
    """Get all beads for a patient."""
    try:
        response = requests.get(
            f"{API_BASE}/beads/context",
            params={"id": patient_id, "depth": 50, "lookup": "reverse"}
        )
        if response.status_code == 200:
            return response.json()
    except Exception as e:
        print(f"  Error getting timeline for {patient_id}: {e}")
    return []


def find_bead_by_content(beads: list, search_terms: list) -> list:
    """Find beads that contain any of the search terms in their content."""
    matching_beads = []
    for bead in beads:
        content_str = json.dumps(bead.get("content", {})).lower()
        for term in search_terms:
            if term.lower() in content_str:
                matching_beads.append(bead)
                break
    return matching_beads


def create_clearance_rule(bead_id: str, denied_roles: list, reason: str, expires_at: str = None):
    """Create a clearance rule for a bead."""
    import uuid

    rule = {
        "id": str(uuid.uuid4()),
        "bead_id": bead_id,
        "denied_roles": denied_roles,
        "created_by": "system_setup",
        "created_at": datetime.now().isoformat(),
        "reason": reason,
    }
    if expires_at:
        rule["expires_at"] = expires_at

    try:
        response = requests.post(f"{API_BASE}/clearance", json=rule)
        if response.status_code in [200, 201]:
            return True
        else:
            print(f"  Failed to create rule: {response.status_code} - {response.text}")
    except Exception as e:
        print(f"  Error creating clearance rule: {e}")
    return False


def main():
    parser = argparse.ArgumentParser(description="Setup sample clearance rules")
    parser.add_argument("--api-url", default="http://localhost:8080", help="MedBeads API URL")
    args = parser.parse_args()

    global API_BASE
    API_BASE = args.api_url

    print("=" * 60)
    print("MedBeads Security Clearance Sample Setup")
    print("=" * 60)

    # First, get all patients
    try:
        response = requests.get(f"{API_BASE}/patients")
        if response.status_code != 200:
            print(f"Failed to get patients: {response.status_code}")
            return
        patients = response.json()
    except Exception as e:
        print(f"Error connecting to API: {e}")
        print("Make sure the MedBeads core server is running.")
        return

    print(f"\nFound {len(patients)} patients")

    # Build patient name -> ID mapping (support both full and abbreviated names)
    patient_map = {}
    for patient in patients:
        name = patient.get("content", {}).get("name", "Unknown")
        patient_map[name] = patient["id"]
        # Also add mapping for "Given Family" format (e.g., "Tanaka Yuki")
        parts = name.split()
        if len(parts) >= 2:
            # "Yuki T" -> also map as "Tanaka Yuki" pattern
            given = parts[0]
            family_initial = parts[-1]
            # Guess full family name from CLEARANCE_SCENARIOS
            for scenario in CLEARANCE_SCENARIOS:
                scenario_name = scenario["patient_name"]
                scenario_parts = scenario_name.split()
                if len(scenario_parts) >= 2:
                    scenario_family = scenario_parts[0]
                    scenario_given = scenario_parts[1]
                    if given == scenario_given and scenario_family.startswith(family_initial.rstrip('.')):
                        patient_map[scenario_name] = patient["id"]
        print(f"  - {name}: {patient['id'][:16]}...")

    # Process each clearance scenario
    print("\n" + "-" * 60)
    print("Setting up clearance rules...")
    print("-" * 60)

    rules_created = 0

    for scenario in CLEARANCE_SCENARIOS:
        patient_name = scenario["patient_name"]

        if patient_name not in patient_map:
            print(f"\n[SKIP] Patient '{patient_name}' not found in database")
            continue

        patient_id = patient_map[patient_name]

        # Get patient's timeline
        timeline = get_patient_timeline(patient_id)
        if not timeline:
            print(f"\n[SKIP] No timeline found for {patient_name}")
            continue

        # Find matching beads
        matching_beads = find_bead_by_content(timeline, scenario["search_terms"])

        if not matching_beads:
            print(f"\n[SKIP] No beads found matching {scenario['search_terms']} for {patient_name}")
            continue

        print(f"\n[{patient_name}] Found {len(matching_beads)} bead(s) matching {scenario['search_terms']}")

        for bead in matching_beads:
            bead_id = bead["id"]
            bead_type = bead.get("type", "unknown")

            success = create_clearance_rule(
                bead_id=bead_id,
                denied_roles=scenario["denied_roles"],
                reason=scenario["reason"],
                expires_at=scenario.get("expires_at")
            )

            if success:
                rules_created += 1
                denied_str = ", ".join(scenario["denied_roles"])
                expires_str = f" (expires: {scenario['expires_at'][:10]})" if scenario.get("expires_at") else ""
                print(f"  ✓ {bead_type}: Denied [{denied_str}]{expires_str}")
            else:
                print(f"  ✗ Failed to create rule for {bead_type}")

    print("\n" + "=" * 60)
    print(f"Setup complete! Created {rules_created} clearance rules.")
    print("=" * 60)


if __name__ == "__main__":
    main()
