import sqlite3
import shutil
import os
import random
import json
from pathlib import Path

# Paths
BASE_DIR = Path(__file__).parent.parent
# Database is in medbeads/core/medbeads_data
DB_PATH = BASE_DIR / "core" / "medbeads_data" / "metadata.db"
OBJECTS_DIR = BASE_DIR / "core" / "medbeads_data" / "objects"
SAMPLE_DIR = BASE_DIR / "sample_data"
SAMPLE_OBJECTS_DIR = SAMPLE_DIR / "objects"
SAMPLE_DB_PATH = SAMPLE_DIR / "metadata.db"

def setup_dirs():
    if SAMPLE_DIR.exists():
        shutil.rmtree(SAMPLE_DIR)
    SAMPLE_OBJECTS_DIR.mkdir(parents=True, exist_ok=True)
    print(f"📂 Created sample directory: {SAMPLE_DIR}")

def get_descendants(conn, root_id):
    """
    Find all descendants of a bead using BFS on bead_edges table.
    """
    descendants = set()
    queue = [root_id]
    
    # Include root
    descendants.add(root_id)

    while queue:
        parent_id = queue.pop(0)
        
        # Find children using the edges table
        cursor = conn.execute("SELECT child_id FROM bead_edges WHERE parent_id = ?", (parent_id,))
        children = [row[0] for row in cursor.fetchall()]
        
        for child in children:
            if child not in descendants:
                descendants.add(child)
                queue.append(child)
                
    return descendants

def extract_sample(count=5):
    if not DB_PATH.exists():
        print(f"❌ Database not found at {DB_PATH}")
        return

    setup_dirs()
    
    conn = sqlite3.connect(DB_PATH)
    
    # 1. Select Random Patients
    print(f"🔍 Selecting {count} random patients...")
    cursor = conn.execute("SELECT id FROM beads WHERE type = 'patient_registration' ORDER BY RANDOM() LIMIT ?", (count,))
    patients = [row[0] for row in cursor.fetchall()]
    
    if not patients:
        print("❌ No patients found!")
        return

    print(f"✅ Selected patients: {patients}")

    all_beads = set()

    # 2. Find all descendants for each patient
    for pid in patients:
        print(f"🔗 Tracing descendants for {pid}...")
        try:
            descendants = get_descendants(conn, pid)
            all_beads.update(descendants)
            print(f"   -> Found {len(descendants)} related beads.")
        except Exception as e:
            print(f"   ❌ Error tracing {pid}: {e}")

    print(f"📦 Total unique beads to extract: {len(all_beads)}")

    # 3. Copy Object Files
    copied_count = 0
    for bead_id in all_beads:
        src = OBJECTS_DIR / bead_id
        dst = SAMPLE_OBJECTS_DIR / bead_id
        
        if src.exists():
            shutil.copy2(src, dst)
            copied_count += 1
        else:
            print(f"⚠️ Missing object file: {bead_id}")

    print(f"✅ Copied {copied_count} object files.")

    # 4. Create Attribution README
    readme_content = """# Sample Data
    
This dataset contains sample medical records for demonstration purposes.

## Attribution
Generated using **Synthea(TM) Patient Generator**.
Synthea is a Synthetic Patient Population Simulator. The data is synthetic and does not represent real patients.

Source: https://github.com/synthetichealth/synthea
    """
    with open(SAMPLE_DIR / "README.md", "w") as f:
        f.write(readme_content)

    conn.close()
    print("🎉 Sample extraction complete!")

if __name__ == "__main__":
    extract_sample()
