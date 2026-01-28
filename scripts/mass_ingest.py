import json
import requests
from datetime import datetime
import os
import tarfile
import argparse
import sys
import io

# Goサーバーのエンドポイント
MEDBEADS_API = "http://localhost:8080/beads"

# ID変換テーブル (FHIRのUUID -> medbeadsのHash)
# ファイルごとにリセットすべきだが、参照解決のためにメモリに保持
# 大量データの場合はRedis等を使うべきだが、今回はlimit制限前提でメモリ辞書
id_map = {}

def post_bead(bead_type, content, parents, timestamp=None):
    """GoサーバーにBeadを送信し、ハッシュIDを取得する"""
    payload = {
        "type": bead_type,
        "content": content,
        "parents": parents,
        "timestamp": timestamp or datetime.now().isoformat()
    }
    
    try:
        res = requests.post(MEDBEADS_API, json=payload)
        res.raise_for_status()
        bead_id = res.json()["id"]
        return bead_id
    except Exception as e:
        print(f"  ❌ Error posting bead: {e}")
        return None

def process_bundle(bundle_data, filename, patient_limit, current_count):
    """1つのFHIR Bundle JSONを処理する"""
    try:
        bundle = json.loads(bundle_data)
    except json.JSONDecodeError:
        print(f"  ❌ Invalid JSON in {filename}")
        return current_count

    entries = bundle.get("entry", [])
    
    # Patientリソースを探す
    patient_entry = next((e for e in entries if e["resource"]["resourceType"] == "Patient"), None)
    if not patient_entry:
        return current_count # Patientがいなければスキップ
    
    current_count += 1
    if current_count > patient_limit:
        return current_count

    pat = patient_entry["resource"]
    pat_id = pat["id"]
    
    # 名前取得
    name_text = "Unknown"
    if "name" in pat and len(pat["name"]) > 0:
        given = pat["name"][0].get("given", [""])[0]
        family = pat["name"][0].get("family", [""])[0]
        name_text = f"{given} {family}"
    
    print(f"  👤 Processing Patient [{current_count}/{patient_limit}]: {name_text} ({filename})")
    
    # Root Bead (Patient)
    root_hash = post_bead(
        bead_type="patient_registration",
        content={"fhir_id": pat_id, "name": name_text, "gender": pat.get("gender")},
        parents=[], 
        timestamp=pat.get("birthDate")
    )
    
    if not root_hash:
        print("  ❌ Failed to create root bead. Skipping.")
        return current_count

    id_map[pat_id] = root_hash
    if "fullUrl" in patient_entry:
        id_map[patient_entry["fullUrl"]] = root_hash

    # 他のリソース処理
    resources = []
    for e in entries:
        res = e["resource"]
        rtype = res["resourceType"]
        if rtype == "Patient": continue
        
        # 日付推定
        date_str = res.get("effectiveDateTime") or res.get("period", {}).get("start") or res.get("authoredOn") or "2099-01-01"
        resources.append({"date": date_str, "data": res, "fullUrl": e.get("fullUrl")})

    resources.sort(key=lambda x: x["date"])

    # 順次登録
    imported_count = 0
    for item in resources:
        res = item["data"]
        rtype = res["resourceType"]
        fhir_id = item.get("fullUrl") or res.get("id")
        
        # 親探しロジック (簡易版)
        parents = []
        
        # 親候補ID
        parent_ref = None
        if rtype == "Encounter":
            parent_ref = pat_id # Encounterの親はPatient
        elif "encounter" in res and "reference" in res["encounter"]:
            parent_ref = res["encounter"]["reference"]
        
        if parent_ref:
            # urn:uuid:等のprefix処理
            clean_ref = parent_ref.replace("urn:uuid:", "")
            
            # 完全一致検索
            if parent_ref in id_map:
                parents.append(id_map[parent_ref])
            # prefix除去検索
            elif clean_ref in id_map:
                parents.append(id_map[clean_ref])
            # Patientへフォールバック
            else:
                parents.append(id_map[pat_id])
        else:
            parents.append(id_map[pat_id])

        # Bead登録
        bead_hash = post_bead(
            bead_type=f"fhir_{rtype.lower()}",
            content=res,
            parents=parents,
            timestamp=item["date"]
        )

        if fhir_id and bead_hash:
            id_map[fhir_id] = bead_hash
            if fhir_id.startswith("urn:uuid:"):
                id_map[fhir_id.replace("urn:uuid:", "")] = bead_hash
        
        imported_count += 1
    
    print(f"    -> Imported {imported_count} records.")
    return current_count

def process_directory(path, limit):
    """ディレクトリ内のJSONファイルを処理する"""
    print(f"📂 Starting ingestion from directory: {path}")
    print(f"🎯 Target limit: {limit} patients")
    
    count = 0
    files = []
    
    # ファイルリスト収集
    for root, dirs, filenames in os.walk(path):
        for filename in filenames:
            if filename.endswith(".json"):
                files.append(os.path.join(root, filename))
    
    files.sort() # 一貫性のためソート
    
    for file_path in files:
        if count >= limit:
            break
            
        try:
            with open(file_path, 'r', encoding='utf-8') as f:
                content = f.read()
                count = process_bundle(content, os.path.basename(file_path), limit, count)
        except Exception as e:
            print(f"  ❌ Error reading {file_path}: {e}")

    print(f"\n🎉 Finished! Imported {count} patients.")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Ingest Synthea FHIR data from tar.gz or directory")
    parser.add_argument("path", help="Path to the tar.gz file or directory")
    parser.add_argument("--limit", type=int, default=5, help="Number of patients to import (default: 5)")
    
    args = parser.parse_args()
    
    if not os.path.exists(args.path):
        print(f"Error: Path not found: {args.path}")
        sys.exit(1)
        
    if os.path.isdir(args.path):
        process_directory(args.path, args.limit)
    else:
        process_tar(args.path, args.limit)
