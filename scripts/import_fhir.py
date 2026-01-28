import json
import requests
from datetime import datetime
import os

# Goサーバーのエンドポイント
MEDBEADS_API = "http://localhost:8081/beads"
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
FHIR_FILE = os.path.join(SCRIPT_DIR, "sample_patient.json")

# ID変換テーブル (FHIRのUUID -> medbeadsのHash)
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
        # print(f"✅ Created: {bead_type} -> {bead_id[:8]}...")
        return bead_id
    except Exception as e:
        print(f"❌ Error posting bead: {e}")
        return None

def main():
    print(f"📂 Loading {FHIR_FILE}...")
    try:
        with open(FHIR_FILE, "r") as f:
            bundle = json.load(f)
    except FileNotFoundError:
        print(f"❌ File not found: {FHIR_FILE}")
        return

    # FHIRリソースを処理しやすいように辞書化 & 日付順にソートしたいが
    # 今回は簡易的に「Patient -> Encounter -> その他」の順で処理する戦略をとる
    
    entries = bundle.get("entry", [])
    
    # 1. Patientリソースを探してルートBeadを作成
    patient_entry = next((e for e in entries if e["resource"]["resourceType"] == "Patient"), None)
    if not patient_entry:
        print("❌ No Patient resource found.")
        return

    pat = patient_entry["resource"]
    pat_id = pat["id"] # FHIRのID (例: urn:uuid:...)
    
    # 名前などの基本情報
    name_text = "Unknown"
    if "name" in pat and len(pat["name"]) > 0:
        given = pat["name"][0].get("given", [""])[0]
        family = pat["name"][0].get("family", [""])[0]
        name_text = f"{given} {family}"
    
    root_hash = post_bead(
        bead_type="patient_registration",
        content={"fhir_id": pat_id, "name": name_text, "gender": pat.get("gender")},
        parents=[], # ルートなので親なし
        timestamp=pat.get("birthDate") # 生年月日をタイムスタンプ代わりに
    )
    
    if not root_hash:
        print("❌ Failed to create root bead. Is server running?")
        return

    # IDマップに登録
    id_map[pat_id] = root_hash
    # fullUrl形式のIDも対応 (Syntheaは uuid:xxx という形式を使う)
    if "fullUrl" in patient_entry:
        id_map[patient_entry["fullUrl"]] = root_hash

    print(f"🌟 Patient Root Created: {root_hash}")

    # 2. 残りのリソースを日付順にソートして処理
    # (FHIRにはresourceTypeごとに日付フィールド名が違うので簡易的な正規化が必要)
    resources = []
    for e in entries:
        res = e["resource"]
        rtype = res["resourceType"]
        if rtype == "Patient": continue
        
        # 日付フィールドの推定
        date_str = res.get("effectiveDateTime") or res.get("period", {}).get("start") or res.get("authoredOn") or "2099-01-01"
        resources.append({"date": date_str, "data": res, "fullUrl": e.get("fullUrl")})

    # 日付順にソート (時系列DAGを作るため)
    resources.sort(key=lambda x: x["date"])

    # 3. 順次登録
    for item in resources:
        res = item["data"]
        rtype = res["resourceType"]
        fhir_id = item.get("fullUrl") or res.get("id")
        
        # 親を探す (Patientへの参照やEncounterへの参照)
        parents = []
        
        # Encounterの場合 -> Patientが親
        if rtype == "Encounter":
            parents.append(id_map[pat_id]) # Patientの子として登録
            
        # MedicationRequestの場合 (明示的に追加)
        elif rtype == "MedicationRequest":
            if "encounter" in res and "reference" in res["encounter"]:
                 enc_ref = res["encounter"]["reference"]
                 if enc_ref in id_map:
                     parents.append(id_map[enc_ref])
                 else:
                     print(f"⚠️ Medication parent not found: {enc_ref}")
                     parents.append(id_map[pat_id])
            else:
                parents.append(id_map[pat_id])

        elif "encounter" in res and "reference" in res["encounter"]:
            enc_ref = res["encounter"]["reference"] # 例: urn:uuid:xyz...
            # IDがなければPatientにつなぐが、基本的にEncounterは先に処理されているはず(日付順なら)
            if enc_ref in id_map:
                parents.append(id_map[enc_ref])
            else:
                parents.append(id_map[pat_id]) 
        else:
            parents.append(id_map[pat_id])

        # Bead作成
        bead_hash = post_bead(
            bead_type=f"fhir_{rtype.lower()}",
            content=res, # FHIRのJSONをそのままPayloadにする
            parents=parents,
            timestamp=item["date"]
        )

        # IDマップに登録 (後続のObservationがこのEncounterを参照できるように)
        if fhir_id and bead_hash:
            id_map[fhir_id] = bead_hash
            # 単純IDもマップ (urn:uuid:を除去したものなど、念のため)
            if fhir_id.startswith("urn:uuid:"):
                 id_map[fhir_id.replace("urn:uuid:", "")] = bead_hash

    print("\n🎉 Migration Complete!")
    print(f"Total Beads: {len(id_map)}")

if __name__ == "__main__":
    main()
