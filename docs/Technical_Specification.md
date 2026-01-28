# MedBeads Technical Specification (v1.0)

**Project Name:** MedBeads

**Version:** 1.0.0-alpha

**License:** Apache License 2.0

**Architect:** Takahito Nakajima (amiac one inc. / University of Tsukuba)

## 1. 概要 (Overview)

MedBeadsは、自律型医療AIエージェント（Medical AI Agents）のために設計された、**不変（Immutable）かつエージェント・ネイティブなデータインフラストラクチャ**である。

従来の電子カルテ（EMR）やFHIR規格が「人間とシステムの相互運用」を主眼に置くのに対し、MedBeadsは「AIの推論プロセス（Context & Reasoning）」と「監査証跡（Auditability）」を**Merkle DAG（有向非巡回グラフ）**として保存することに特化している。

---

## 2. システムアーキテクチャ (System Architecture)

システムは、パフォーマンスと柔軟性を両立させるため、以下の3層構造（Tiered Architecture）を採用する。

| **Layer**          | **Component**          | **Language**           | **Role**                                                                  |
| ------------------------ | ---------------------------- | ---------------------------- | ------------------------------------------------------------------------------- |
| **L1: Core**       | **MedBeads Engine**    | **Go (Golang)**        | データのハッシュ化、CAS保存、グラフ探索、インデックス管理。並行処理性能を重視。 |
| **L2: Middleware** | **Intelligence Layer** | **Python (FastAPI)**   | LLM (Gemini) との対話、DICOM解析、FHIRデータの変換・移行。                      |
| **L3: Frontend**   | **Visualizer**         | **React (TypeScript)** | グラフ構造の可視化 (Graph View)、タイムライン表示、医師向けUI。                 |

![Workflow Diagram](concept-image.jpeg)

---

## 3. データ構造定義 (The Bead Definition)

MedBeadsにおける最小の情報単位を **"Bead"** と定義する。

BeadはJSONとしてシリアライズされ、その**コンテンツのハッシュ値（SHA-256）**がそのまま一意なID（Content Addressing）となる。

### 3.1. JSON Schema

**JSON**

```
{
  "id": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "type": "medical_note",
  "timestamp": "2026-01-26T10:00:00Z",
  "author": "did:medbeads:doctor:12345",
  "parents": [
    "sha256:parent_hash_a...",
    "sha256:parent_hash_b..."
  ],
  "content": {
    "summary": "患者は呼吸苦を訴え来院。右肺野に浸潤影を認める。",
    "body_text": "【現病歴】...（長文カルテ）...",
    "structured": {
      "diagnosis": "Pneumonia",
      "icd10": "J18.9"
    }
  },
  "evidence": [
    {
      "uri": "s3://hospital-pacs/2026/ct_chest_001.dcm",
      "mime_type": "application/dicom",
      "hash": "sha256:image_file_hash..."
    }
  ],
  "signature": "base64:digital_signature..."
}
```

### 3.2. フィールド詳細仕様

| **Field**        | **Type** | **Description**                                                                                                             | **Storage Policy**        |
| ---------------------- | -------------- | --------------------------------------------------------------------------------------------------------------------------------- | ------------------------------- |
| **`id`**       | String         | SHA-256ハッシュ値。改ざん検知の根幹。                                                                                             | Index (SQLite)                  |
| **`type`**     | String         | イベントの種類 (`data_ingest`,`reasoning`,`plan`等)。                                                                       | Index (SQLite)                  |
| **`parents`**  | Array          | 直前の関連BeadのIDリスト。これにより**DAG（文脈）**を形成する。                                                                   | Index (SQLite)                  |
| **`content`**  | Object         | **AIが即座に読むべき「意味」データ。**``テキストカルテ（長文含む）、数値、要約、AIの思考過程は全てここに格納する。   | **In-Bead (JSON)**        |
| **`evidence`** | Array          | **巨大なバイナリデータへの参照。**``画像(DICOM)、波形、PDF等は外部ストレージに置き、リンクとハッシュのみを記録する。 | **Off-Chain (Reference)** |

---

## 4. ストレージ戦略 (Storage Strategy)

MedBeadsは、**「データの不変性」 **と** 「検索の高速性」**を両立させるため、**Hybrid Storage Model** を採用する。

### 4.1. Content Addressable Storage (CAS) - The Truth

* **役割:** データの「正本（Single Source of Truth）」。
* **実装:** ローカルファイルシステム（またはS3）上のフラットなファイル群。
* **パス:** `./objects/{hash_prefix}/{hash_rest}` (例: `objects/a1/b2c3...`)
* **特性:**  **Write-Once, Read-Many** 。一度書き込まれたファイルは二度と変更されない（削除も原則行わない）。

### 4.2. Metadata Index (SQLite) - The Cache

* **役割:** クエリ（検索）の高速化。
* **実装:** SQLite (`metadata.db`)。
* **データ:** `id`, `type`, `timestamp`, `parents` のみを抽出して格納。
* **特性:**  **Ephemeral (使い捨て可能)** 。CAS上のファイルさえあれば、`Reindex` プロセスによりいつでも完全に再構築できる。

---

## 5. コアアルゴリズム (Core Algorithms)

### 5.1. ハッシュ生成 (Immutability)

データの同一性を保証するため、正規化されたJSONに対してハッシュを計算する。

$$
ID = \text{SHA256}(\text{CanonicalJSON}(Bead_{\text{content, parents, type}}))
$$

### 5.2. コンテキスト取得 (Context Retrieval via BFS)

AIエージェントがある時点（Bead）の判断根拠を知るためのグラフ探索。

1. 指定された `Bead ID` を始点とする。
2. `parents` フィールドを再帰的に辿る（幅優先探索）。
3. 重複を排除し、時系列（またはトポロジカル順）にソートされたBeadのリストを返す。

* **利点:** AIは「全患者データ」を検索する必要がなく、**「その診断に至るまでに関連したサブグラフ」**だけを効率的に読み込める（Lazy Loading）。

---

## 6. API仕様 (Go Core Interface)

### `POST /beads`

* **機能:** 新しいBeadを作成・保存する。
* **Input:** JSON Payload (Content, Type, Parents)
* **Process:** ハッシュ計算 -> CAS保存 -> SQLiteインデックス登録
* **Output:** `{"id": "sha256:..."}`

### `GET /beads?id={hash}`

* **機能:** 特定のBeadの実体（Raw JSON）を取得する。

### `GET /beads/context?id={hash}&depth={n}`

* **機能:** 指定したBeadから、深さ `n` までグラフを遡り、文脈全体（Beadのリスト）を取得する。
* **Use Case:** LLMが「なぜこの診断になったか？」を分析する際、このエンドポイントを叩く。

---

## 7. FHIRとの互換性 (Interoperability)

既存医療システムとの共存のため、以下の移行ロジックを定義する。

1. **Patient Resource:** グラフのルート（Genesis Bead）となる。
2. **Mapping:** FHIRの `uuid` または `url` を、MedBeadsの `hash ID` に変換するマッピングテーブルをメモリ上で保持しながら変換する。
3. **Topology:**
   * `Encounter` → `Patient` を親とする。
   * `Observation` / `Condition` → 対応する `Encounter` を親とする（Encounterがない場合はPatient）。
   * これにより、フラットなFHIRリソース群が、**意味のあるツリー構造**に再構築される。

---

## 8. Data Mapping (FHIR to MedBeads)

MedBeadsはFHIR形式のデータを内部的な「Bead」構造に変換して扱います。
以下は、FHIRリソースタイプがMedBeadsのどのタイプにマッピングされ、UIでどのように表示されるかの対応表です。

### 8.1. 基本構造

| FHIR Resource | MedBeads Type | UI表示 (Timeline) | 備考 |
| :--- | :--- | :--- | :--- |
| `Patient` | `patient_registration` | **Encounter** | 患者登録イベントとして扱われます |
| `Encounter` | `encounter` / `fhir_encounter` | **Encounter** | 外来・入院などの受診記録 |
| `Condition` | `condition` / `fhir_condition` | **Condition** | 診断された病名・状態 |
| `Observation` | `observation` / `fhir_observation` | **Observation** | 検査結果、バイタルサイン |
| `MedicationRequest` | `medication` / `fhir_medicationrequest` | **Medication** | 処方薬情報 |
| `DiagnosticReport` | `diagnostic_report` / `fhir_diagnosticreport` | **Diagnostic Report** | 検査レポート |
| `DocumentReference` | `fhir_documentreference` | **Clinical Note** | 臨床ノート、退院サマリー |
| `Procedure` | `fhir_procedure` | **Procedure** | 手術・処置 (今回追加) |
| `Immunization` | `fhir_immunization` | **Immunization** | 予防接種 (今回追加) |
| `ImagingStudy` | `fhir_imagingstudy` | **Imaging Study** | 画像検査メタデータ (今回追加) |

### 8.2. 詳細マッピング

#### 1. Patient -> patient_registration
*   **Date**: `birthDate` または取込日時
*   **Data Fields**:
    *   `name`: `name` (Given + Family)
    *   `gender`: `gender`
    *   `birthDate`: `birthDate`

#### 2. Encounter
*   **Date**: `period.start`
*   **Data Fields**:
    *   `department`: `type[0].text`
    *   `encounter_type`: `class.code` (e.g., outpatient, emergency)
    *   `chief_complaint`: `type[0].text` または `reasonCode`

#### 3. Condition
*   **Date**: `recordedDate`
*   **Data Fields**:
    *   `condition_name`: `code.text`
    *   `clinical_status`: `clinicalStatus.coding[0].code` (active, resolved, etc.)

#### 4. MedicationRequest
*   **Date**: `authoredOn`
*   **Data Fields**:
    *   `medication_name`: `medicationCodeableConcept.text`
    *   `dosage`: `dosageInstruction[0].text`

#### 5. Observation
*   **Date**: `effectiveDateTime`
*   **Data Fields**:
    *   `display_name`: `code.text`
    *   `value`: `valueQuantity.value` + `unit` または `valueString`
    *   `interpretation`: `interpretation.coding[0].code` (H -> abnormal, etc.)

#### 6. DiagnosticReport & DocumentReference
*   **Date**: `effectiveDateTime` / `date`
*   **Data Fields**:
    *   `findings`: Base64エンコードされた `content.attachment.data` または `presentedForm.data` をデコードして表示
    *   **※自動整形**: "Chief Complaint", "Plan" などのヘッダーを検出し、Markdownで見やすく整形

#### 7. Procedure
*   **Date**: `performedDateTime` / `performedPeriod.start`
*   **Data Fields**:
    *   `procedure_name`: `code.text`
    *   `status`: `status`
    *   `outcome`: `outcome.text`

#### 8. Immunization
*   **Date**: `occurrenceDateTime`
*   **Data Fields**:
    *   `vaccine_name`: `vaccineCode.text`
    *   `route`: `route.text`

### 8.3. フィルタリングされているリソース
以下のリソースはMedBeads内部には保存されていますが、現在のTimeline（`api.ts`）では**表示対象外**です。

*   `Claim` (保険請求)
*   `ExplanationOfBenefit` (給付説明)
*   `CarePlan` (ケアプラン)
*   `CareTeam` (医療チーム)
*   `Device` (使用デバイス)
*   `SupplyDelivery` (物品配送)
*   `Provenance` (データの来歴)

---

### [Internal Note for Developers]

* **Text Handling:** 医師のカルテ記事など、テキストデータはどんなに長くても `content` 内に入れてください。外部ファイルにするとAIの検索速度が低下します。
* **Concurrency:** Goのサーバーはステートレス（状態を持たない）設計にし、書き込みの競合はCAS（ファイル名ハッシュ）の性質により自然に回避してください。
