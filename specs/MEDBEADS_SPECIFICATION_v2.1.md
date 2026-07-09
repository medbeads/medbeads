# MedBeads Specification v2.1

**Project Name:** MedBeads — An Agent-Native, Immutable Data Substrate for Trustworthy Medical AI

**Version:** 2.1.0-draft

**Date:** 2026-02-20

**Author:** Takahito Nakajima (amiac one inc. / University of Tsukuba)

**License:** Apache License 2.0

---

## 目次

1. [概要](#1-概要)
2. [設計原則](#2-設計原則)
3. [システムアーキテクチャ](#3-システムアーキテクチャ)
4. [Bead定義](#4-bead定義)
5. [Bead Type体系](#5-bead-type体系)
6. [Antigens（表面マーカー）](#6-antigens表面マーカー)
7. [DAG構造仕様](#7-dag構造仕様)
8. [Sibling Beads仕様](#8-sibling-beads仕様)
9. [APCデーモン仕様](#9-apcデーモン仕様)
10. [ストレージ戦略](#10-ストレージ戦略)
11. [SQLiteスキーマ](#11-sqliteスキーマ)
12. [コアアルゴリズム](#12-コアアルゴリズム)
13. [API仕様](#13-api仕様)
14. [検索・参照の3層構造](#14-検索参照の3層構造)
15. [コンテキストバンドル](#15-コンテキストバンドル)
16. [Triad Agents](#16-triad-agents)
17. [処方適正量チェック](#17-処方適正量チェック)
18. [セキュリティとアクセス制御](#18-セキュリティとアクセス制御)
19. [FHIRとの互換性](#19-fhirとの互換性)
20. [監査・再現性](#20-監査再現性)
21. [フロントエンド仕様](#21-フロントエンド仕様)
22. [用語集](#22-用語集)

---

## 1. 概要

MedBeadsは、自律型医療AIエージェントのために設計された**不変（Immutable）かつエージェント・ネイティブなデータインフラストラクチャ**である。

従来の電子カルテ（EMR）やFHIR規格が「人間とシステムの相互運用」を主眼に置くのに対し、MedBeadsは「AIの推論プロセス（Context & Reasoning）」と「監査証跡（Auditability）」を**Merkle DAG（有向非巡回グラフ）**として保存することに特化する。

### 1.1 核心思想

**"MedBeads is the Language, MCP is the Interface"**

- **MedBeads**: データそのものを「文脈を持つ粒子（Bead）」として再定義し、真正性（Hash Chain）と連結性（Context）を担保するプロトコル。LLMにとっての「文脈言語」。
- **MCP (Model Context Protocol)**: LLMエージェントがこの「MedBeads言語」を話すためのインターフェース。MedBeads（確実性）とVectorDB（曖昧性）を統合し、エージェントに最適な情報を提供するゲートウェイ。

### 1.2 MedBeadsが解決する課題

| 課題 | MedBeadsのアプローチ |
| --- | --- |
| AIが「何を見て判断したか」が不明 | DAGの因果グラフにより、判断に使った全入力を暗号学的に証明 |
| データ改ざんのリスク | SHA-256 CASにより、一度記録されたデータは変更不能 |
| 関連データの取りこぼし | 芋づる式検索（Chain Retrieval）で1件から関連記録を網羅的に取得 |
| 処方チェックの根拠が不透明 | prescription_check Beadのparentsを辿ることで全入力を再現可能 |
| 横方向の臨床的関連が見えない | Sibling Beads（Antigens + APC）で薬物相互作用等を自動検出 |
| 誰が何を見られるか制御できない | Beadレベルのブラックリスト型Security Clearanceにより、ロール単位でアクセスを制御。精神科記録を保険会社から隠す、がん疑いを告知前に患者・家族から一時制限する等、臨床現場の要件に対応。緊急時は全制限をオーバーライドし、その事実を監査ログに記録 |

---

## 2. 設計原則

1. **不変性（Immutability）** — 一度記録されたBeadは二度と変更されない。修正は新しいBeadの作成で表現する
2. **コンテンツアドレッシング（CAS）** — BeadのIDはそのコンテンツのSHA-256ハッシュ。同一内容は常に同一IDを持つ
3. **決定論的コンテキスト取得** — 患者情報を確率的検索（RAG）ではなく、DAGの因果グラフ走査により確定的に取得する
4. **データと推論の分離** — MedBeadsがコンテキストの完全性を保証し、LLM/ルールエンジンが推論を担当する
5. **完全監査可能性** — 全操作の入力・推論・出力が再現可能
6. **Multi-Parent DAG** — 一つのBeadが複数の親を持てる。これにより複雑な医療の因果関係を自然に表現
7. **免疫系アナロジー** — Beadが持つAntigens（表面マーカー）に基づき、APCデーモンが横方向の関連を自動検出

---

## 3. システムアーキテクチャ

### 3.1 5層構成

```text
┌──────────────────────────────────────────────────────┐
│  L5: Triad Agents                                    │
│  - Decomposer / Navigator / Verifier                 │
│  - 処方チェックエージェント                              │
└──────────────┬───────────────────────────────────────┘
               │
┌──────────────▼───────────────────────────────────────┐
│  L4: MCP Server (The Orchestrator)                   │
│  - 3層検索のオーケストレーション                         │
│  - コンテキストバンドル構築                               │
└──────────────┬───────────────────────────────────────┘
               │
        ┌──────┴──────────────────────┐
        │                             │
        ▼                             ▼
┌───────────────────────┐  ┌───────────────────────┐
│ L1: Go Core (:8080)   │  │ L2: Python AI (:8000) │
│ - CAS (SHA-256)       │  │ - FastAPI             │
│ - SQLite Index        │  │ - Gemini / LLM        │
│ - FTS5 Search         │  │ - Insight生成          │
│ - Graph Traversal     │  │                       │
│ - APC Daemon          │  │                       │
│ - Security Clearance  │  │                       │
└───────────┬───────────┘  └───────────────────────┘
            │
            ▼
┌───────────────────────┐  ┌───────────────────────┐
│ L3: React Frontend    │  │ Vector Search (:8788) │
│ - Timeline            │  │ - ruri-v3 埋め込み     │
│ - GraphView           │  │ - Faiss検索           │
│ - DetailPanel         │  │                       │
│ - Clearance UI        │  │                       │
└───────────────────────┘  └───────────────────────┘
```

### 3.2 コンポーネント詳細

| Layer | Component | Language | Role |
| --- | --- | --- | --- |
| L1: Core | MedBeads Engine | Go (Golang) | データハッシュ化、CAS保存、グラフ探索、インデックス管理、APCデーモン |
| L2: Middleware | Intelligence Layer | Python (FastAPI) | LLM対話、FHIR変換、Insight生成 |
| L3: Frontend | Visualizer | React (TypeScript) | グラフ可視化、タイムライン、Sibling表示 |
| L4: Orchestrator | MCP Server | Python | 3層検索オーケストレーション、コンテキストバンドル構築 |
| L5: Agents | Triad Agents | Python (LLM) | 分解・探索・検証の3エージェント構成 |
| Aux | Vector Search | Python | 意味的類似検索（ruri-v3 + Faiss） |

---

## 4. Bead定義

MedBeadsにおける最小の情報単位を **"Bead"** と定義する。BeadはJSONとしてシリアライズされ、その**コンテンツのSHA-256ハッシュ**がそのまま一意なIDとなる。

### 4.1 Beadスキーマ

```json
{
  "id": "sha256:e3b0c44298fc1c149afbf4c8996fb924...",
  "type": "medical_record",
  "timestamp": "2026-01-26T10:00:00Z",
  "author": "did:medbeads:doctor:12345",
  "parents": [
    "sha256:parent_hash_a...",
    "sha256:parent_hash_b..."
  ],
  "antigens": [
    "organ:renal",
    "risk:nephrotoxic",
    "rxnorm:855332"
  ],
  "content": {
    "summary": "...",
    "body_text": "...",
    "structured": { }
  },
  "evidence": [
    {
      "uri": "s3://hospital-pacs/2026/ct_chest_001.dcm",
      "mime_type": "application/dicom",
      "hash": "sha256:image_file_hash..."
    }
  ],
  "clearance": {
    "denied_roles": ["insurance", "researcher"],
    "reason": "精神科記録",
    "expires_at": null
  },
  "signature": "base64:digital_signature..."
}
```

### 4.2 フィールド仕様

| Field | Type | Required | Description | ハッシュ対象 |
| --- | --- | --- | --- | --- |
| `id` | string | auto | SHA-256ハッシュ値。改ざん検知の根幹 | N/A（計算結果） |
| `type` | string | yes | Beadの種別（Section 5参照） | yes |
| `timestamp` | string | yes | ISO 8601形式の日時 | yes |
| `author` | string | no | 作成者のDID | no |
| `parents` | []string | yes | 親BeadのIDリスト。DAGエッジを形成 | yes |
| `antigens` | []string | yes | 表面マーカー。APCデーモンのマッチング対象（Section 6参照） | yes |
| `content` | object | yes | AIが読むべき意味データ。テキスト・数値・構造化データ | yes |
| `evidence` | []object | no | 巨大バイナリへの参照（DICOM、PDF等） | no |
| `clearance` | object | no | アクセス制御設定 | no |
| `signature` | string | no | デジタル署名 | no |

### 4.3 ハッシュ生成

```text
ID = SHA-256(CanonicalJSON({type, timestamp, parents, antigens, content}))
```

- `author`, `evidence`, `clearance`, `signature` はハッシュ対象外（メタデータとして後付け可能）
- CanonicalJSON: キーをソートし、空白を除去した正規化JSON

### 4.4 型定義

#### Go Core（正規定義）

```go
type Bead struct {
    ID        string                 `json:"id,omitempty"`
    Type      string                 `json:"type"`
    Timestamp string                 `json:"timestamp"`
    Author    string                 `json:"author,omitempty"`
    Parents   []string               `json:"parents"`
    Antigens  []string               `json:"antigens,omitempty"`
    Content   map[string]interface{} `json:"content"`
    Evidence  []Evidence             `json:"evidence,omitempty"`
    Clearance *ClearanceSettings     `json:"clearance,omitempty"`
    Signature string                 `json:"signature,omitempty"`
}

type ClearanceSettings struct {
    DeniedRoles []string `json:"denied_roles"`
    Reason      string   `json:"reason,omitempty"`
    ExpiresAt   *string  `json:"expires_at,omitempty"`
}

type Evidence struct {
    URI      string `json:"uri"`
    MimeType string `json:"mime_type"`
    Hash     string `json:"hash"`
}
```

#### TypeScript Frontend

```typescript
export interface Bead {
  id: string;
  type: string;
  timestamp: string;
  author?: string;
  parents: string[];
  antigens?: string[];
  content: any;
  evidence?: Evidence[];
  clearance?: ClearanceSettings;
  signature?: string;
}

export interface ClearanceSettings {
  denied_roles: ViewerRole[];
  reason?: string;
  expires_at?: string;
}

export interface Evidence {
  uri: string;
  mime_type: string;
  hash: string;
}
```

#### Python

```python
@dataclass
class MedicalBead:
    id: str
    type: str
    timestamp: str
    parents: List[str]
    antigens: List[str] = field(default_factory=list)
    author: str = ""
    patient_id: str = ""
    content: Dict[str, Any] = field(default_factory=dict)
    evidence: List[Dict[str, str]] = field(default_factory=list)
    clearance: Optional[Dict[str, Any]] = None
    signature: str = ""
```

---

## 5. Bead Type体系

### 5.1 構造系タイプ

| Type | 説明 | 例 |
| --- | --- | --- |
| `patient_registration` | 患者のDAGルートノード。全記録の起点 | 入院時初回登録 |
| `daily_summary` | 日次グルーピングノード。同日の記録の共通親 | 2025-01-10の記録群 |

### 5.2 臨床記録タイプ（EMR-CSV由来）

| Type | 判定条件 | 例 |
| --- | --- | --- |
| `admission` | 「入院」を含む | 入院時診察記録 |
| `discharge` | 「退院」を含む | 退院時サマリー |
| `medical_record` | 上記いずれにも該当しないデフォルト | 経過記録、看護記録 |
| `vital_signs` | 「バイタル」を含む | 体温・血圧・SpO2 |
| `lab_results` | 「検査」「採血」「血液培養」を含む | 血液検査、画像検査 |
| `imaging` | 「画像」「CT」「X線」「超音波」を含む | CT画像レポート |
| `prescription` | 「処方」「投薬」「抗生剤投与」等を含む | 薬剤処方 |
| `prescription_update` | 「処方変更」を含む | 用量変更、薬剤変更 |
| `surgery` | 「手術」を含む | 手術実施記録 |
| `anesthesia_record` | 「麻酔」を含む | 術中麻酔記録 |
| `pre_op_record` | 「術前」を含む | 術前評価記録 |
| `post_op_note` | 「術後」を含む | 術後経過記録 |
| `incident` | 「インシデント」「緊急」「異常」「事故」を含む | インシデント報告 |
| `rehabilitation` | 「リハビリ」を含む | リハビリ記録 |
| `health_check` | 「健診」「健康診断」を含む | 健診結果 |
| `consultation` | 「コンサル」を含む | コンサルテーション記録 |
| `icu_record` | 「ICU」を含む | ICU経過記録 |

### 5.3 FHIR系タイプ

| Type | FHIR Resource | 主なcontent |
| --- | --- | --- |
| `fhir_encounter` | Encounter | class, period, reason, status |
| `fhir_observation` | Observation | category, code(LOINC), component[], effectiveDateTime |
| `fhir_condition` | Condition | code(SNOMED), clinicalStatus, onsetDateTime |
| `fhir_medicationrequest` | MedicationRequest | authoredOn, dosageInstruction, medicationReference |
| `fhir_medication` | Medication | code(RxNorm) |
| `fhir_procedure` | Procedure | code, performedPeriod, status |
| `fhir_immunization` | Immunization | vaccineCode, date, status |
| `fhir_imagingstudy` | ImagingStudy | modality, description, started |
| `fhir_documentreference` | DocumentReference | type, content, date |
| `fhir_diagnosticreport` | DiagnosticReport | code, result, effectiveDateTime |
| `fhir_goal` | Goal | description, status, addresses |
| `fhir_organization` | Organization | name, type |

### 5.4 処方チェック系タイプ

| Type | 説明 | 主な親リンク |
| --- | --- | --- |
| `drug_master` | 薬剤マスター情報（添付文書由来） | 前版の `drug_master`（改訂履歴チェーン） |
| `drug_master_source` | マスターデータのソース情報 | なし（Genesis的） |
| `prescription_order` | 処方オーダー | `fhir_encounter`, `fhir_condition` |
| `prescription_check` | 処方チェック結果（AI判定） | `prescription_order`, `drug_master`, 関連 `fhir_observation` |
| `dose_adjustment` | 用量調整推奨 | `prescription_check`, 腎/肝機能の `fhir_observation` |
| `interaction_alert` | 相互作用アラート | 複数の `prescription_order` |
| `dispensing_record` | 調剤記録 | `prescription_order`, `prescription_check` |
| `prescriber_override` | 医師によるアラート確認・承認 | `prescription_check` |

### 5.5 関連付け系タイプ

| Type | 説明 |
| --- | --- |
| `sibling_link` | 横方向の関連を記録する専用Bead（Section 8参照） |

---

## 6. Antigens（表面マーカー）

### 6.1 概要

Antigensは、Beadが持つ**検索可能なマーカータグ**である。免疫系の細胞膜抗原に着想を得ており、APCデーモンがBeadを巡回してantigenマッチに基づきSibling Link Beadを自動生成する。

### 6.2 特性

- Bead作成時に確定 → SHA-256ハッシュに含まれる → CASの不変性を維持
- 1つのBeadが複数のantigensを持てる
- 後から変更不可（変更する場合は新しいBeadを作成）

### 6.3 Namespace体系

| Namespace | 由来 | 例 | 説明 |
| --- | --- | --- | --- |
| `snomed:<code>` | SNOMED CT | `snomed:444814009` | 病名・処置のコード |
| `loinc:<code>` | LOINC | `loinc:55284-4` | 検査項目コード |
| `rxnorm:<code>` | RxNorm | `rxnorm:745679` | 薬剤コード |
| `atc:<code>` | ATC分類 | `atc:B01AA03` | 薬効分類コード |
| `organ:<system>` | 臓器系統 | `organ:renal` | 関連臓器・系統 |
| `risk:<type>` | リスクカテゴリ | `risk:bleeding` | 臨床リスク分類 |
| `actor:<id>` | 関与者 | `actor:tanaka_md` | 記録者・実施者 |
| `temporal:<tag>` | 時間的文脈 | `temporal:pre_op` | 臨床的タイミング |

### 6.4 定義済みantigen値

#### organ namespace

| Antigen | 説明 |
| --- | --- |
| `organ:renal` | 腎臓・腎機能 |
| `organ:hepatic` | 肝臓・肝機能 |
| `organ:cardiovascular` | 心血管系 |
| `organ:respiratory` | 呼吸器系 |
| `organ:gastrointestinal` | 消化器系 |
| `organ:neurological` | 神経系 |
| `organ:hematologic` | 血液・造血系 |
| `organ:endocrine` | 内分泌系 |
| `organ:musculoskeletal` | 筋骨格系 |

#### risk namespace

| Antigen | 説明 |
| --- | --- |
| `risk:bleeding` | 出血リスク |
| `risk:nephrotoxic` | 腎毒性リスク |
| `risk:hepatotoxic` | 肝毒性リスク |
| `risk:qt_prolongation` | QT延長リスク |
| `risk:serotonin_syndrome` | セロトニン症候群リスク |
| `risk:hypoglycemia` | 低血糖リスク |
| `risk:infection` | 感染リスク |
| `risk:fall` | 転倒リスク |
| `risk:delirium` | せん妄リスク |
| `risk:respiratory_depression` | 呼吸抑制リスク |

#### temporal namespace

| Antigen | 説明 |
| --- | --- |
| `temporal:pre_op` | 術前 |
| `temporal:intra_op` | 術中 |
| `temporal:post_op` | 術後 |
| `temporal:admission` | 入院時 |
| `temporal:discharge` | 退院時 |
| `temporal:emergency` | 緊急時 |

### 6.5 Antigen自動抽出ルール

#### FHIR系Beadからの抽出

| Beadの条件 | 抽出antigen |
| --- | --- |
| `content.code.coding[].system == "http://snomed.info/sct"` | `snomed:<code>` |
| `content.code.coding[].system == "http://loinc.org"` | `loinc:<code>` |
| `content.code.coding[].system == ".../rxnorm"` | `rxnorm:<code>` |
| `content.category[].coding[].code == "vital-signs"` | `organ:cardiovascular`（血圧の場合） |
| display に腎機能関連語を含む | `organ:renal` |
| `type == "fhir_medicationrequest"` | `risk:*`（薬剤リスクDBから自動付与） |

#### EMR-CSV系Beadからの抽出

| Beadのtype / content | 抽出antigen |
| --- | --- |
| `type == "surgery"` | `temporal:intra_op` |
| `type == "anesthesia_record"` | `temporal:intra_op`, `risk:respiratory_depression` |
| `type == "post_op_note"` | `temporal:post_op` |
| `type == "pre_op_record"` | `temporal:pre_op` |
| `type == "prescription"` + 「ワーファリン」 | `rxnorm:<code>`, `risk:bleeding`, `organ:hematologic` |
| `type == "lab_results"` + 「eGFR」 | `loinc:69405-9`, `organ:renal` |
| `type == "vital_signs"` | `organ:cardiovascular` |
| `type == "incident"` | `risk:*`（内容に応じて自動分類） |

### 6.6 Antigen具体例

```json
{
  "type": "prescription",
  "timestamp": "2025-01-10T09:00:00",
  "parents": ["<daily_summary_hash>"],
  "antigens": [
    "rxnorm:855332",
    "atc:J01DH02",
    "risk:nephrotoxic",
    "organ:renal",
    "risk:infection",
    "actor:tanaka_md",
    "temporal:post_op"
  ],
  "content": {
    "drug": "メロペネム 1g",
    "route": "点滴静注",
    "frequency": "8時間毎",
    "recorder": "田中医師"
  }
}
```

---

## 7. DAG構造仕様

### 7.1 Edge Rule（親子関係の決定ルール）

各beadの `parents[]` は以下の優先順位で決定する。

| 優先度 | 条件 | 親 | 説明 |
| --- | --- | --- | --- |
| 1 | `patient_registration` | なし（ルート） | DAGの根。患者ごとに1つ |
| 2 | `daily_summary` | `patient_registration` | 日付ごとのグルーピングノード |
| 3 | `prescription_update` | 元の `prescription` + `daily_summary` | 処方変更チェーンを形成 |
| 4 | `anesthesia_record`, `post_op_note` | 対応する `surgery` | 手術コンテキストを一体化 |
| 5 | `surgery` | `daily_summary` | 手術は日次サマリーの子 |
| 6 | その他すべて | `daily_summary` | 同日の記録はsiblingsになる |

### 7.2 設計原則

1. **Patient Root原則**: すべてのbeadは `patient_registration` を最上位祖先に持つ。`findPatientRoot` で患者の全記録ツリーにアクセス可能
2. **Daily Summary原則**: 同日の記録は `daily_summary` を共通の親に持つ。siblingsが自然に発生し「同時期の関連記録」が芋づる式に取得可能
3. **Domain Chain原則**: 処方変更・手術関連など、ドメイン固有のチェーンは専用の親子関係を形成
4. **Multi-Parent DAG**: 一つのbeadが複数の親を持てる（例: `prescription_update` は元の処方と日次サマリーの両方を親に持つ）

### 7.3 DAGトポロジ

```text
patient_registration [P-001: 田中一郎]
├── daily_summary [2025-01-10]
│   ├── admission [入院記録]
│   │   antigens: [temporal:admission, organ:gastrointestinal]
│   ├── vital_signs [入院時バイタル]
│   │   antigens: [organ:cardiovascular, loinc:55284-4]
│   ├── lab_results [入院時血液検査 eGFR=45]
│   │   antigens: [loinc:69405-9, organ:renal, risk:nephrotoxic]
│   └── prescription [Rx: セファゾリン1g]
│       antigens: [rxnorm:XXX, risk:infection]
│
├── daily_summary [2025-01-11]
│   ├── surgery [胃切除術]
│   │   antigens: [snomed:287816003, temporal:intra_op, organ:gastrointestinal]
│   │   ├── anesthesia_record [全身麻酔記録]
│   │   │   antigens: [temporal:intra_op, risk:respiratory_depression]
│   │   └── post_op_note [術後経過]
│   │       antigens: [temporal:post_op]
│   ├── vital_signs [術後バイタル]
│   │   antigens: [organ:cardiovascular]
│   ├── prescription [Rx: フェンタニル]
│   │   antigens: [rxnorm:4337, risk:respiratory_depression, temporal:post_op]
│   └── prescription_update [Rx変更: セファゾリン→メロペネム]
│       parents: [prescription(セファゾリン), daily_summary(01-11)]
│       antigens: [rxnorm:855332, risk:nephrotoxic, organ:renal]
│
│   *** Sibling Link Beads (APCデーモンが自動生成) ***
│   ├── sibling_link [lab_results(eGFR) ↔ prescription(メロペネム)]
│   │   matched_antigen: "organ:renal"
│   │   relation: "clinical_correlation"
│   │   severity: "warning"
│   │
│   └── sibling_link [anesthesia_record ↔ prescription(フェンタニル)]
│       matched_antigen: "risk:respiratory_depression"
│       relation: "drug_interaction"
│       severity: "alert"
│
├── daily_summary [2025-01-12]
│   ├── vital_signs [POD1バイタル]
│   ├── lab_results [術後血液検査]
│   └── medical_record [経過記録]
│
└── discharge [退院記録]
```

### 7.4 トラバーサルパターン

| パターン | 起点 | 方向 | 取得内容 |
| --- | --- | --- | --- |
| Ancestor Chain | 任意のbead | 上方向 | daily_summary → patient_registration（患者特定） |
| Sibling Expansion | 任意のbead | 横方向 | 同じ daily_summary を親に持つ同日の全記録 |
| Descendant Tree | surgery bead | 下方向 | 麻酔記録、術後記録（手術の全容） |
| Prescription Chain | prescription | 下方向 | 処方変更の履歴（用量変更、薬剤変更） |
| Full Patient Tree | patient_registration | 下方向 | 患者の全記録ツリー |
| Explicit Sibling | 任意のbead | 横方向 | Sibling Link Beadで結ばれた臨床的関連のある記録 |

### 7.5 親bead解決ロジック

```python
def resolve_parent(record, existing_beads):
    """レコードの種別・日時・内容に基づいて適切な親beadを決定"""

    rec_type = record.get("記録種別", "")
    timestamp = record.get("記録日時", "")
    date = timestamp[:10]  # YYYY-MM-DD

    # 1. 患者登録 → ルート（親なし）
    if "入院" in rec_type and is_first_admission(record):
        return [patient_root_id]

    # 2. 処方変更 → 元の処方を親に
    if "処方" in rec_type:
        original_rx = find_original_prescription(record, existing_beads)
        if original_rx:
            return [original_rx, daily_summary_id]

    # 3. 手術関連 → 手術beadを親に
    if rec_type in ["麻酔記録", "術後記録", "手術看護記録"]:
        surgery_bead = find_surgery_bead(date, existing_beads)
        if surgery_bead:
            return [surgery_bead]

    # 4. デフォルト → 日次サマリーを親に（siblingsが発生）
    return [get_or_create_daily_summary(date, existing_beads)]
```

---

## 8. Sibling Beads仕様

### 8.1 2種類のSibling

| 種類 | 定義 | 検出方法 |
| --- | --- | --- |
| 暗黙的Sibling (Implicit) | 同じparentを持つBead同士 | `GetSiblings()` で動的に算出 |
| 明示的Sibling (Explicit) | Sibling Link Beadによって記録された関係 | `edge_type='sibling'` でインデックス |

### 8.2 Sibling Link Beadの定義

```json
{
  "type": "sibling_link",
  "timestamp": "2025-01-10T10:05:00",
  "parents": ["<bead_A_hash>", "<bead_B_hash>"],
  "antigens": ["risk:nephrotoxic_correlation", "alert:dose_adjust"],
  "content": {
    "matched_antigen": "organ:renal",
    "relation": "clinical_correlation",
    "severity": "warning",
    "description": "メロペネム投与中にeGFR 45（低値）— 腎機能に基づく用量調整の検討が必要",
    "detected_by": "apc_daemon",
    "scan_generation": 1
  }
}
```

### 8.3 Sibling Link Beadのフィールド仕様

| フィールド | 型 | 説明 |
| --- | --- | --- |
| `type` | string | 常に `"sibling_link"` |
| `timestamp` | string | リンク作成日時（ISO 8601） |
| `parents` | []string | 関連付けされるBeadのID（2つ以上） |
| `antigens` | []string | このリンク自身のantigen（二次応答の対象） |
| `content.matched_antigen` | string | マッチのトリガーとなったantigen |
| `content.relation` | string | 関係タイプ（下表参照） |
| `content.severity` | string | 重要度: `info`, `warning`, `alert`, `critical` |
| `content.description` | string | 人間可読な関連性の説明 |
| `content.detected_by` | string | 検出者: `apc_daemon`, `manual`, `agent` |
| `content.scan_generation` | int | APCデーモンの何周目のスキャンで生成されたか |

### 8.4 Relation Type定義

| Relation | 説明 | 例 |
| --- | --- | --- |
| `drug_interaction` | 薬物相互作用 | ワーファリン ↔ NSAIDs |
| `contraindication` | 禁忌 | MAO阻害剤 ↔ SSRI |
| `clinical_correlation` | 臨床的関連 | eGFR低下 ↔ 腎排泄薬 |
| `causal` | 因果関係 | 検査異常値 → 処方変更 |
| `temporal_correlation` | 時間的相関 | 同時刻の異なる記録 |
| `contradiction` | 矛盾 | インシデント報告 ↔ EMR記録 |
| `corroborating` | 裏付け | 複数の記録が同一事実を支持 |
| `alternative` | 代替 | 処方A中止 → 処方B開始 |
| `dose_related` | 用量関連 | 腎機能 ↔ 用量調整 |
| `monitoring` | モニタリング | TDM結果 ↔ 薬剤処方 |

### 8.5 Severity定義

| Severity | 色 | 意味 | 例 |
| --- | --- | --- | --- |
| `info` | 青 | 参考情報 | 同時刻の記録の関連付け |
| `warning` | 黄 | 注意喚起 | 腎機能低下と腎排泄薬の併用 |
| `alert` | 橙 | 要確認 | 相互作用の可能性 |
| `critical` | 赤 | 重大 | 禁忌の組み合わせ、矛盾の検出 |

### 8.6 Edge登録ロジック

Sibling Link Bead作成時に以下のedgeを登録する:

```text
Sibling Link Bead (SL) の parents: [A, B]

登録されるedge:
  (SL, A, parent)   -- SLの親はA
  (SL, B, parent)   -- SLの親はB
  (A, B, sibling)   -- AとBは横方向で関連
  (B, A, sibling)   -- BとAは横方向で関連（双方向）
```

### 8.7 CAS不変性の保証

- Sibling Link Bead自体がCASに保存される → ハッシュが確定する → 改ざん不能
- 元のBead A, Bは変更されない（antigensはBead作成時に確定済み）
- 「誰が、いつ、なぜこの2つを関連づけたか」が監査可能なレコードとして残る
- Sibling Link Bead自身もantigensを持つため、**二次応答（リンクのリンク）** が自然に発生しうる

---

## 9. APCデーモン仕様

### 9.1 概要

APC (Antigen Presenting Cell) デーモンは、MedBeads Coreサーバー内でバックグラウンドに常駐するgoroutineとして動作する。Beadを巡回し、antigenマッチに基づいてSibling Link Beadを自動生成する。

免疫系のアナロジー:

| 免疫系 | MedBeads |
| --- | --- |
| 細胞膜抗原 (Surface Antigen) | Beadが持つ `antigens[]` |
| 抗原提示細胞 (APC) | APCデーモン（マッチングエンジン） |
| 免疫応答（抗体結合） | Sibling Link Beadの生成 |
| 抗体の特異性 | マッチした抗原の種類 = relation type |
| 免疫カスケード | Sibling Link Bead自身のantigensによる連鎖反応 |

### 9.2 動作フロー

```text
loop:
  1. 未スキャンBeadを1つ取得（bead_apc_scanテーブル参照）
  2. そのBeadのantigensを読む
  3. 親チェーンを遡ってコンテキスト（患者ID等）を取得
  4. 同一antigenを持つ他のBeadを検索（bead_antigensインデックス使用）
  5. 同一患者内のペアに絞り込む
  6. ペアの臨床的関連性をスコアリング
  7. スコアが閾値超え → Sibling Link Bead生成
  8. スキャン済みとしてマーク
  9. sleep(idle_interval)
end loop

※ 新Bead登録時にも即座にトリガー可能（Event-Driven Mode）
```

### 9.3 マッチングルール

#### 基本ルール

```text
IF bead_A.antigens ∩ bead_B.antigens ≠ ∅
AND bead_A.patient == bead_B.patient
AND NOT already_linked(bead_A, bead_B)
THEN score = compute_relevance(bead_A, bead_B, matched_antigens)
```

#### スコアリング

| 条件 | スコア加算 |
| --- | --- |
| 共通antigenが1つ | +1 |
| 共通antigenが2つ以上 | +2 per additional |
| risk namespace のマッチ | +3（臨床的重要度が高い） |
| organ namespace のマッチ | +2 |
| temporal namespace のマッチ | +1 |
| 時間的近接性（24時間以内） | +2 |
| 時間的近接性（7日以内） | +1 |
| 一方がprescription、他方がlab_results | +3（処方と検査の関連） |

#### 閾値

| 条件 | 値 |
| --- | --- |
| 生成閾値 | スコア >= 4 で Sibling Link Bead 生成 |
| 重複防止 | 同一ペア x 同一 matched_antigen の組は1つだけ |

### 9.4 増殖制御パラメータ

| パラメータ | デフォルト値 | 説明 |
| --- | --- | --- |
| `max_sibling_depth` | 2 | Sibling Link BeadのLink（二次応答）の最大深度 |
| `max_siblings_per_bead` | 10 | 1つのBeadから生成されるSibling Linkの最大数 |
| `min_score_threshold` | 4 | マッチスコアの最低閾値 |
| `idle_interval` | 5s | スキャン間のスリープ時間 |
| `batch_size` | 10 | 1回のスキャンで処理するBead数 |
| `secondary_response_decay` | 0.5 | 二次応答のスコアに乗算する減衰係数 |

### 9.5 Event-Driven Mode

新しいBeadが `POST /beads` で登録された際、APCデーモンに即座に通知し、待機時間なしでスキャンを開始する。

```go
func (apc *APCDaemon) OnBeadCreated(beadID string) {
    apc.priorityQueue <- beadID
}
```

---

## 10. ストレージ戦略

### 10.1 CAS (Content Addressable Storage) — The Truth

- **役割:** データの正本（Single Source of Truth）
- **実装:** ローカルファイルシステム（またはS3）
- **パス:** `./objects/{hash_prefix}/{hash_rest}`
- **特性:** Write-Once, Read-Many。一度書き込まれたファイルは変更されない

### 10.2 Metadata Index (SQLite) — The Cache

- **役割:** クエリ（検索）の高速化
- **実装:** SQLite (`metadata.db`)
- **特性:** Ephemeral（使い捨て可能）。CASファイルから `Reindex` で完全に再構築可能

---

## 11. SQLiteスキーマ

```sql
-- ==========================================
-- メタデータテーブル
-- ==========================================
CREATE TABLE beads (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    parents TEXT,                    -- JSON array: ["hash1", "hash2"]
    antigens TEXT DEFAULT '[]',     -- JSON array: ["organ:renal", ...]
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    content_text TEXT
);
CREATE INDEX idx_type ON beads(type);
CREATE INDEX idx_timestamp ON beads(timestamp);

-- ==========================================
-- FTS5全文検索（trigram）
-- ==========================================
CREATE VIRTUAL TABLE beads_fts USING fts5(
    id UNINDEXED, content, tokenize='trigram'
);

-- ==========================================
-- エッジテーブル（親子 + sibling関係）
-- ==========================================
CREATE TABLE bead_edges (
    child_id   TEXT NOT NULL,
    parent_id  TEXT NOT NULL,
    edge_type  TEXT NOT NULL DEFAULT 'parent',  -- 'parent' or 'sibling'
    PRIMARY KEY (child_id, parent_id, edge_type)
);
CREATE INDEX idx_edge_parent ON bead_edges(parent_id);
CREATE INDEX idx_edge_type ON bead_edges(edge_type);

-- ==========================================
-- Antigensインデックステーブル（正規化）
-- ==========================================
CREATE TABLE bead_antigens (
    bead_id  TEXT NOT NULL,
    antigen  TEXT NOT NULL,
    PRIMARY KEY (bead_id, antigen)
);
CREATE INDEX idx_antigen ON bead_antigens(antigen);

-- ==========================================
-- APCデーモン スキャン管理
-- ==========================================
CREATE TABLE bead_apc_scan (
    bead_id        TEXT PRIMARY KEY,
    scanned_at     TEXT,
    scan_generation INTEGER DEFAULT 0,
    sibling_count  INTEGER DEFAULT 0
);

-- ==========================================
-- Security Clearance
-- ==========================================
CREATE TABLE clearance_rules (
    id TEXT PRIMARY KEY,
    bead_id TEXT NOT NULL,
    denied_roles TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    reason TEXT,
    expires_at DATETIME
);
CREATE INDEX idx_clearance_bead ON clearance_rules(bead_id);

-- ==========================================
-- 監査ログ
-- ==========================================
CREATE TABLE clearance_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    bead_id TEXT NOT NULL,
    action TEXT NOT NULL,
    user_id TEXT NOT NULL,
    user_roles TEXT NOT NULL,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    details TEXT
);
```

---

## 12. コアアルゴリズム

### 12.1 ハッシュ生成

```text
ID = SHA-256(CanonicalJSON({type, timestamp, parents, antigens, content}))
```

### 12.2 コンテキスト取得 (BFS)

1. 指定された Bead ID を始点とする
2. `parents` フィールドを再帰的に辿る（幅優先探索）
3. 重複を排除し、時系列またはトポロジカル順にソートされたBeadリストを返す
4. **reverse lookup**: 指定Beadを親に持つ子Beadを辿る（下方向）

### 12.3 Sibling取得

```text
GetSiblings(bead_id):
  1. Implicit: 対象beadのparentsを取得 → 各parentの全childrenを取得 → 自分自身を除外
  2. Explicit: edge_type='sibling' で bead_id に関連するBeadを取得
  3. 両者を統合して返す（sibling_type: "implicit" / "explicit" を付与）
```

### 12.4 SaveToCAS フロー

```text
1. CanonicalJSON生成 → SHA-256計算 → id確定
2. CASファイル書き込み（objects/{prefix}/{rest}）
3. beadsテーブルにINSERT
4. bead_edgesテーブルにparent edge登録
5. bead_antigensテーブルに各antigenをINSERT
6. beads_ftsに全文テキスト登録
7. type == "sibling_link" の場合、parents間のsibling edgeを双方向で登録
8. APCデーモンに通知（Event-Driven Mode）
```

---

## 13. API仕様

### 13.1 Bead操作

| Method | Endpoint | 説明 |
| --- | --- | --- |
| `POST` | `/beads` | Bead作成。`antigens` フィールド受付。作成後APCデーモンに通知 |
| `GET` | `/beads?id=<hash>` | Bead取得（antigens含む） |
| `GET` | `/beads/context?id=<hash>&depth=<n>[&lookup=reverse]` | 祖先/子孫チェーン取得 |
| `GET` | `/beads/siblings?id=<hash>` | Sibling取得（implicit + explicit統合） |
| `GET` | `/beads/sibling-links?id=<hash>` | 指定Beadに関連するSibling Link Bead一覧 |

### 13.2 検索

| Method | Endpoint | 説明 |
| --- | --- | --- |
| `GET` | `/search?q=<text>[&resourceTypes=...]` | 全文検索 |
| `GET` | `/search/fts?q=<text>&limit=<n>` | FTS5 trigram検索 |
| `GET` | `/search/structured?...` | 構造化検索 |
| `GET` | `/antigens/search?antigen=<value>&patient=<id>` | 特定antigenを持つBead検索 |

### 13.3 患者

| Method | Endpoint | 説明 |
| --- | --- | --- |
| `GET` | `/patients` | 全患者（patient_registration）一覧 |
| `GET` | `/resource-counts` | リソースタイプ別件数 |

### 13.4 Security Clearance

| Method | Endpoint | 説明 |
| --- | --- | --- |
| `GET` | `/clearance?bead_id=<id>` | Clearanceルール取得 |
| `POST` | `/clearance` | Clearanceルール作成 |
| `DELETE` | `/clearance?id=<rule_id>` | Clearanceルール削除 |
| `GET` | `/clearance/check?bead_id=<id>` | アクセス権チェック |
| `GET` | `/roles` | 利用可能なViewerロール一覧 |

### 13.5 APCデーモン

| Method | Endpoint | 説明 |
| --- | --- | --- |
| `GET` | `/apc/status` | スキャン済みBead数、生成済みSibling Link数、キュー長 |
| `POST` | `/apc/trigger` | APCデーモンの手動トリガー（開発・デバッグ用） |

### 13.6 Siblings APIレスポンス例

`GET /beads/siblings?id=<hash>`

```json
[
  {
    "id": "abc123...",
    "type": "lab_results",
    "sibling_type": "explicit",
    "relation": "clinical_correlation",
    "severity": "warning",
    "matched_antigen": "organ:renal",
    "timestamp": "2025-01-10T10:00:00",
    "antigens": ["loinc:69405-9", "organ:renal", "risk:nephrotoxic"],
    "content": { "test": "eGFR", "value": "45" }
  },
  {
    "id": "def456...",
    "type": "vital_signs",
    "sibling_type": "implicit",
    "timestamp": "2025-01-10T08:00:00",
    "antigens": ["organ:cardiovascular"],
    "content": { "bp": "120/80" }
  }
]
```

---

## 14. 検索・参照の3層構造

エージェントからの問い合わせに対し、MCPは以下の3層をオーケストレーションする。

### Layer 1: Anchor Search (MedBeads QR / FTS)

- **役割**: 確実な起点（Anchor）を見つける
- **手法**: Go Core Server による高速な全文検索（FTS5）と構造化クエリ
- **ユースケース**: 「2024年10月15日の記録」「田中医師の記述」

### Layer 2: Semantic Expansion (Vector Search)

- **役割**: 意味的な広がりを持たせる
- **手法**: Layer 1で見つかったAnchorを起点に、意味的に類似したBeadを探す
- **フィルタリング**: MedBeadsで絞り込んだIDリスト内でベクトル検索（Pre-filtering）
- **ユースケース**: 「患者の様子がおかしい記述」「急変の予兆」

### Layer 3: Context Chaining (MedBeads Traversal)

- **役割**: 「点」を「線」にする。MedBeadsの真骨頂
- **手法**: 特定されたBeadの ancestors / descendants / siblings を辿り、前後の文脈を取得
- **提供価値**: 単なる事実だけでなく「なぜそうなったか（前）」「どうなったか（後）」をLLMに提供

---

## 15. コンテキストバンドル

### 15.1 `_build_context_bundle()` 仕様

```python
async def _build_context_bundle(medbeads_client, bead_id, depth=2):
    seen = set()

    # 1. 自身
    bead = await client.get_bead_by_id(bead_id)
    seen.add(bead_id)

    # 2. 祖先チェーン（上方向）
    ancestors = await client.get_context(bead_id, depth=depth)
    for a in ancestors:
        seen.add(a["id"])

    # 3. 子孫チェーン（下方向）
    descendants = await client.get_context(bead_id, depth=depth, lookup="reverse")
    for d in descendants:
        seen.add(d["id"])

    # 4. siblings + 再帰的siblings（1段階）
    siblings = await client.get_siblings(bead_id)
    extended_siblings = []
    for sib in siblings:
        if sib["id"] not in seen:
            seen.add(sib["id"])
            extended_siblings.append(sib)
            # siblingsのsiblings（同じ親の他の記録）
            sib_siblings = await client.get_siblings(sib["id"])
            for ss in sib_siblings:
                if ss["id"] not in seen:
                    seen.add(ss["id"])
                    extended_siblings.append(ss)

    return {
        "bead": bead,
        "ancestors": ancestors,
        "descendants": descendants,
        "siblings": extended_siblings,
        "total_related": len(seen)
    }
```

### 15.2 芋づる式検索の動作例

```text
1. エージェントが "メロペネム1g" で検索
2. 3層検索 → prescription_update bead がヒット
3. _build_context_bundle() が実行:
   a. ancestors: prescription_update → 元の prescription (セファゾリン)
                 → daily_summary [01-11] → patient_registration
   b. siblings: daily_summary [01-11] の子
                → surgery, vital_signs, prescription (フェンタニル)
   c. explicit siblings: Sibling Link経由
                → lab_results (eGFR=45)  [organ:renal マッチ]
   d. descendants: (この例ではなし)
4. → 手術日の全コンテキスト + 臨床的に関連する検査結果が一括取得される
```

---

## 16. Triad Agents

MCPを通じてMedBeadsを利用する3つの専門エージェント。

### 16.1 Agent A: The Decomposer（分解者）

- **入力**: 事故報告書案（Report）
- **役割**: 報告書を検証可能な「事実命題（Claim Beads）」に分解する
- **MCP利用**: なし（テキスト解析のみ）

### 16.2 Agent B: The Navigator（探索者）

- **入力**: Claim Bead（例: 「14:30 プロポフォール投与」）
- **役割**: 事実に対応する「真実の文脈（Truth Chain）」をEMRから発掘する
- **MCP利用**: `search_bead` (QR/Vector) → `get_context` (Chain)

### 16.3 Agent C: The Verifier（検証者）

- **入力**: Claim Bead vs Truth Chain
- **役割**: 報告書の記述がカルテの文脈と整合しているか判定
- **判定**: 「記載漏れ」「時間矛盾」「因果関係の誤り」

---

## 17. 処方適正量チェック

### 17.1 drug_master Beadスキーマ

```json
{
  "id": "sha256:...",
  "type": "drug_master",
  "timestamp": "2026-01-15T00:00:00Z",
  "author": "did:medbeads:system:pmda-importer",
  "parents": ["sha256:<previous_version_hash>"],
  "antigens": ["rxnorm:XXX", "atc:J01XA01"],
  "content": {
    "drug_name": "バンコマイシン塩酸塩",
    "yj_code": "6110400A1020",
    "atc_code": "J01XA01",
    "source": "PMDA添付文書",
    "source_revision_date": "2025-12-01",
    "standard_dosage": {
      "adult": {
        "dose_per_kg": { "min": 15, "max": 20, "unit": "mg/kg" },
        "frequency": "q12h",
        "max_daily_dose": { "value": 4000, "unit": "mg" },
        "route": "IV"
      },
      "pediatric": {
        "dose_per_kg": { "min": 10, "max": 15, "unit": "mg/kg" },
        "frequency": "q6h",
        "max_daily_dose": null,
        "route": "IV"
      }
    },
    "renal_adjustment": [
      { "egfr_min": 30, "egfr_max": 59, "adjustment": "q24h に延長" },
      { "egfr_min": 10, "egfr_max": 29, "adjustment": "q48h に延長" },
      { "egfr_min": 0, "egfr_max": 9, "adjustment": "TDM必須、個別設定" }
    ],
    "hepatic_adjustment": "通常不要",
    "contraindications": ["本剤過敏症の既往"],
    "interactions": [
      {
        "drug": "アミノグリコシド系",
        "severity": "重大",
        "effect": "腎毒性・聴器毒性増強"
      }
    ],
    "tdm_required": true,
    "therapeutic_range": {
      "trough": { "min": 10, "max": 20, "unit": "μg/mL" },
      "auc_24": { "min": 400, "max": 600, "unit": "μg·h/mL" }
    }
  },
  "evidence": [
    {
      "uri": "https://www.pmda.go.jp/...",
      "mime_type": "application/xml",
      "hash": "sha256:<pmda_xml_hash>"
    }
  ],
  "signature": "base64:..."
}
```

### 17.2 薬剤マスターデータソース階層

| Layer | ソース | 更新頻度 | Bead Type |
| --- | --- | --- | --- |
| L1 | PMDA添付文書XML | 改訂時（不定期） | `drug_master` |
| L2 | 腎機能別用量調整表 | 年次 | `drug_master`（拡張） |
| L3 | 診療ガイドライン | 年次〜数年 | `drug_master`（拡張） |
| L4 | 施設採用薬・院内レジメン | 随時 | `drug_master_local` |
| L5 | LLM補完知識 | リアルタイム | `drug_master_inferred`（要検証フラグ） |

### 17.3 マスター改訂チェーン

```text
drug_master v1 (2025-06)
  hash: sha256:aaa...
       ↓
drug_master v2 (2025-12) ← PMDA改訂
  hash: sha256:bbb...
  parents: [sha256:aaa...]  ← 暗号学的に前版を参照
       ↓
drug_master v3 (2026-06) ← 次回改訂
  hash: sha256:ccc...
  parents: [sha256:bbb...]
```

### 17.4 処方チェックMCPツール

| ツール名 | 説明 | 入力 | 出力 |
| --- | --- | --- | --- |
| `check_dose` | 用量適正チェック | drug_name, dose, route, patient_bead_id | prescription_check Bead |
| `check_interaction` | 相互作用チェック | patient_bead_id | interaction_alert Bead |
| `get_recommended_dose` | 推奨用量取得 | drug_name, indication, patient_bead_id | dose_adjustment Bead |
| `check_renal_dose` | 腎機能別用量調整 | drug_name, patient_bead_id | dose_adjustment Bead |
| `verify_master_integrity` | マスターデータ整合性検証 | drug_master_bead_id | 検証結果 |
| `get_drug_master` | 薬剤マスター取得 | drug_name or yj_code | drug_master Bead |

### 17.5 check_dose ワークフロー

```text
1. prescription_order Beadを受信
2. DAG traversal（BFS）で患者コンテキスト取得:
   - 体重（Observation Bead）
   - 腎機能 eGFR（Observation Bead）
   - 肝機能（Observation Bead）
   - 年齢（Patient Genesis Bead）
   - 併用薬（他の prescription_order Beads）
   - アレルギー（Condition Beads）
3. drug_master Beadをハッシュで取得・整合性検証
4. チェックロジック実行:
   a. 用量範囲チェック（体重ベース）
   b. 腎機能調整チェック
   c. 肝機能調整チェック
   d. 相互作用チェック
   e. 禁忌チェック
   f. TDM要否チェック
   g. 年齢別チェック（小児・高齢者）
5. prescription_check Beadを生成・DAGに追加
6. CRITICAL/ALERTの場合、処方医に通知
```

### 17.6 処方フローDAG

```text
Patient Genesis Bead
  └── Encounter Bead（外来受診）
        ├── Observation Bead（体重 60kg）
        ├── Observation Bead（eGFR 45）
        ├── Observation Bead（血液培養 MRSA+）
        ├── Condition Bead（MRSA菌血症）
        │     └── prescription_order Bead（バンコマイシン 1g q12h）
        │           ├── parent: drug_master Bead（バンコマイシン v2026.01）
        │           └── prescription_check Bead（AI判定: ALERT）
        │                 ├── parents: [処方, マスター, eGFR, 体重, 併用薬]
        │                 ├── status: "ALERT - q24hへ変更推奨"
        │                 └── dose_adjustment Bead（修正処方案）
        │                       └── prescriber_override Bead（医師承認）
        │                             └── dispensing_record Bead（調剤実施）
        └── prescription_order Bead（ゲンタマイシン 80mg q8h）
              └── interaction_alert Bead
                    └── parents: [VCM処方, GM処方, drug_master x 2]
```

---

## 18. セキュリティとアクセス制御

### 18.1 Security Clearanceモデル

MedBeadsは**ブラックリストモデル**を採用する。

- **デフォルト状態**: すべてのBeadはすべてのロールからアクセス可能
- **制限方法**: 特定のロールを明示的に拒否（deny）する
- **緊急時オーバーライド**: `emergency` および `system` ロールは全制限を無条件でバイパス

```text
ブラックリストモデルの判定フロー:

1. viewerのロールが emergency または system か？
   → YES: アクセス許可（全制限をバイパス）
2. 対象Beadにclearance設定があるか？
   → NO: アクセス許可（制限なし = 全員閲覧可能）
3. clearanceに有効期限があり、期限切れか？
   → YES: アクセス許可（期限切れルールは無視）
4. viewerのロールがdenied_rolesに含まれるか？
   → YES: アクセス拒否
   → NO: アクセス許可
```

### 18.2 Viewerロール

| Role | Label (日本語) | 説明 | 制限対象にできるか |
| --- | --- | --- | --- |
| `patient` | 患者本人 | 患者本人 | Yes |
| `family` | 家族 | 患者の家族 | Yes |
| `primary_care` | 主治医 | かかりつけ医 | Yes |
| `specialist` | 専門医 | コンサルテーション医 | Yes |
| `nurse` | 看護師 | 看護スタッフ | Yes |
| `insurance` | 保険会社 | 保険者・審査機関 | Yes |
| `researcher` | 研究者 | 研究アクセス | Yes |
| `emergency` | 緊急時 | 救急時オーバーライド | **No（常にアクセス可能）** |
| `system` | システム | システム/AI処理 | **No（常にアクセス可能）** |

### 18.3 Clearance設定の2つの方式

MedBeadsは2つのClearance設定方式をサポートする。

#### 方式1: Embedded Clearance（Bead内蔵 — 推奨）

Bead作成時に `clearance` フィールドとして直接埋め込む。CASに保存されるためDB参照不要で高速。

```json
{
  "id": "sha256:abc123...",
  "type": "fhir_condition",
  "content": { "code": { "text": "Major depressive disorder" } },
  "clearance": {
    "denied_roles": ["insurance", "family"],
    "reason": "Patient privacy request - mental health",
    "expires_at": null
  }
}
```

#### 方式2: Legacy DB Rules（後付けルール）

既存のBeadに対して `POST /clearance` APIで後からルールを追加する。Beadの再作成が不要なため、運用中の制限変更に使用する。

```json
{
  "id": "rule_abc123",
  "bead_id": "sha256:target_bead...",
  "denied_roles": ["patient", "family"],
  "created_by": "tanaka_md",
  "reason": "Awaiting patient counseling session",
  "expires_at": "2026-03-01T00:00:00Z"
}
```

#### アクセスチェックの優先順位

```text
1. Emergency/System ロール → 無条件で許可
2. Embedded Clearance（bead.clearance）を確認 → O(1)
3. Legacy DB Rules（clearance_rulesテーブル）を確認 → O(N)
4. いずれにも該当しない → 許可（ブラックリストモデル）
```

### 18.4 ClearanceSettingsフィールド仕様

| フィールド | 型 | 必須 | 説明 |
| --- | --- | --- | --- |
| `denied_roles` | []string | Yes | アクセスを拒否するロールの配列 |
| `reason` | string | No | 制限理由（人間可読な説明） |
| `expires_at` | string (ISO 8601) | No | 制限の有効期限。null = 恒久的制限 |

**有効期限のロジック:**
- `expires_at` が null/空: 制限は**恒久的**
- `expires_at` が設定済みかつ現在時刻 > expires_at: ルールは**無視**される（期限切れ）

### 18.5 ゴーストレコード方式

アクセス拒否されたBeadは**完全に非表示にするのではなく、存在は見せるが内容を隠す**（Ghost Record）。

```text
┌──────────────────────────────┐
│  Timeline View               │
│                              │
│  ● 2026-01-10 入院記録       │  ← 閲覧可能
│  ● 2026-01-10 バイタル       │  ← 閲覧可能
│  ◉ 2026-01-10 ████████       │  ← ゴーストレコード（存在は見える）
│  ● 2026-01-11 手術記録       │  ← 閲覧可能
│                              │
└──────────────────────────────┘

DetailPanelで選択した場合:
┌──────────────────────────────┐
│  🔒 Access Denied            │
│                              │
│  Your current role (insurance)│
│  does not have permission to │
│  view this information.      │
│                              │
│  Restricted for: 保険会社     │
│  Reason: 精神科記録           │
└──────────────────────────────┘
```

**設計意図:**
- GraphViewでDAGの構造（ノード数・接続関係）を正確に描画するため、Beadの存在自体は返す
- 内容の秘匿はフロントエンド（DetailPanel）が担当
- 「この患者には制限された記録が存在する」という事実自体は、構造的に把握可能

### 18.6 監査ログ

すべてのClearance操作は `clearance_audit` テーブルに記録される。

| 監査イベント | 発生条件 | 記録内容 |
| --- | --- | --- |
| `created` | Clearanceルール作成時 | bead_id, denied_roles, 作成者, 理由 |
| `deleted` | Clearanceルール削除時 | rule_id, 削除者 |
| `emergency_access` | Emergency ロールが制限Beadにアクセス | bead_id, アクセス者, ロール |

```sql
CREATE TABLE clearance_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    bead_id TEXT NOT NULL,
    action TEXT NOT NULL,       -- 'created', 'deleted', 'emergency_access'
    user_id TEXT NOT NULL,
    user_roles TEXT NOT NULL,   -- JSON array
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    details TEXT
);
```

**Emergency Accessの特別扱い:**
- `emergency` ロールは全制限をバイパスできるが、その事実は**必ず監査ログに記録**される
- これにより「誰が、いつ、緊急権限で何にアクセスしたか」が完全に追跡可能
- 不正な緊急アクセスの事後検証が可能

### 18.7 臨床シナリオ例

#### シナリオA: 精神科記録 — 保険会社からの保護

**患者:** 鈴木健司（40代男性）、うつ病・不安障害で治療中

| Bead | denied_roles | reason | expires_at |
| --- | --- | --- | --- |
| fhir_condition（うつ病） | `["insurance"]` | 精神科情報のプライバシー保護 | null（恒久） |
| fhir_medicationrequest（エスシタロプラム） | `["insurance"]` | 精神科薬剤 — 制限 | null（恒久） |
| fhir_documentreference（経過記録） | `["insurance", "family"]` | 治療詳細の保護 | null（恒久） |

**アクセスマトリクス:**

| ロール | 病名 | 処方 | 経過記録 |
| --- | --- | --- | --- |
| primary_care | 閲覧可 | 閲覧可 | 閲覧可 |
| specialist | 閲覧可 | 閲覧可 | 閲覧可 |
| nurse | 閲覧可 | 閲覧可 | 閲覧可 |
| insurance | **拒否** | **拒否** | **拒否** |
| family | 閲覧可 | 閲覧可 | **拒否** |
| patient | 閲覧可 | 閲覧可 | 閲覧可 |
| emergency | 閲覧可 | 閲覧可 | 閲覧可 |

#### シナリオB: がん疑い — 告知前の一時制限

**患者:** 山本太郎（50代男性）、PSA高値で前立腺がん疑い

| Bead | denied_roles | reason | expires_at |
| --- | --- | --- | --- |
| fhir_condition（前立腺がん疑い） | `["patient", "family"]` | 告知カウンセリング前 | **14日後** |
| fhir_observation（PSA値） | `["patient", "family"]` | 告知カウンセリング前 | **14日後** |

**ポイント:** `expires_at` により14日後に制限が自動解除。医師がカウンセリングの準備を整えるための猶予期間。

#### シナリオC: 婦人科 — 家族からの保護

**患者:** 田中由紀（30代女性）、不正性器出血で婦人科受診

| Bead | denied_roles | reason | expires_at |
| --- | --- | --- | --- |
| fhir_condition（不正性器出血） | `["family"]` | 婦人科情報のプライバシー | null |
| fhir_observation（妊娠検査） | `["family"]` | 婦人科情報のプライバシー | null |

#### シナリオD: 救急 — 高度制限（主治医のみ）

**患者:** 中村亮（20代男性）、ER搬送。薬物スクリーニング陽性

| Bead | denied_roles | reason | expires_at |
| --- | --- | --- | --- |
| fhir_observation（THC陽性） | `["family", "insurance", "specialist", "nurse"]` | 法的リスク — 主治医のみ | null |
| fhir_observation（血中アルコール） | `["family", "insurance"]` | 雇用・法的リスク | null |

**ポイント:** THC検出結果は `primary_care` と `emergency` と `system` のみがアクセス可能（ほぼ全ロールを拒否）。

#### シナリオE: 一般 — 制限なし

**患者:** 佐藤花子（60代女性）、定期健康診断

clearance設定なし。全ロールが全Beadにアクセス可能。

### 18.8 処方チェック文脈の権限

| Role | アクセス可能データ | 処方チェック権限 |
| --- | --- | --- |
| `prescriber` | 全患者データ + 全マスター | チェック実行・オーバーライド |
| `pharmacist` | 全患者データ + 全マスター | チェック実行・疑義照会発行 |
| `nurse` | 担当患者データ | チェック結果閲覧のみ |
| `ai_agent` | clearance許可範囲の患者データ | チェック実行（推奨のみ、決定権なし） |
| `insurance` | 処方情報のみ（臨床詳細除外） | アクセス不可 |
| `patient` | 自身のデータ（チェック結果含む） | 閲覧のみ |

### 18.9 AI Agentの権限制約

- AI Agentは `prescription_check` Beadを生成できるが、`prescription_order` Beadの生成・変更は不可
- AI判定が `CRITICAL` の場合、処方医による明示的な確認Bead（`prescriber_override`）がないと調剤に進めない
- `prescriber_override` Beadには医師のDID署名が必須

### 18.10 セキュリティ設計の保証

| 保証 | 説明 |
| --- | --- |
| **不変性** | Embedded ClearanceはBeadのメタデータとしてCASに保存され、改ざん不能 |
| **監査可能性** | 全Clearance操作（作成・削除・緊急アクセス）が監査ログに記録 |
| **緊急アクセス** | 常に可能（ただし監査ログに必ず記録） |
| **粒度** | Bead単位 x ロール単位の細粒度制御 |
| **時間制御** | `expires_at` による自動期限切れ |
| **ゴーストレコード** | 存在は見せるが内容は隠す（DAG構造の完全性を維持） |

---

## 19. FHIRとの互換性

### 19.1 変換ルール

1. **Patient Resource**: グラフのルート（Genesis Bead = `patient_registration`）
2. **Mapping**: FHIRの `uuid`/`url` を MedBeadsの `hash ID` に変換するマッピングテーブルを保持
3. **Topology**:
   - `Encounter` → `Patient` を親
   - `Observation` / `Condition` → 対応する `Encounter` を親（Encounterがない場合はPatient）

### 19.2 FHIR Resource → MedBeads Type マッピング

| FHIR Resource | MedBeads Type | Date Field |
| --- | --- | --- |
| Patient | `patient_registration` | birthDate |
| Encounter | `fhir_encounter` | period.start |
| Condition | `fhir_condition` | recordedDate |
| MedicationRequest | `fhir_medicationrequest` | authoredOn |
| Observation | `fhir_observation` | effectiveDateTime |
| DiagnosticReport | `fhir_diagnosticreport` | effectiveDateTime |
| DocumentReference | `fhir_documentreference` | date |
| Procedure | `fhir_procedure` | performedDateTime |
| Immunization | `fhir_immunization` | occurrenceDateTime |
| ImagingStudy | `fhir_imagingstudy` | started |

---

## 20. 監査・再現性

### 20.1 監査クエリ

| 質問 | DAG操作 |
| --- | --- |
| 「この処方チェックの根拠は？」 | prescription_check Beadのparentsを走査 |
| 「チェック時点のeGFRは？」 | parentsに含まれるObservation Beadを取得 |
| 「使用したマスターのバージョンは？」 | parentsに含まれるdrug_master Beadを取得 |
| 「マスターは改竄されていないか？」 | SHA-256再計算で検証 |
| 「医師はアラートを確認したか？」 | prescriber_override Beadの存在・署名を検証 |
| 「同じ入力で再チェックした結果は？」 | 同一parents群で再実行、結果を比較 |
| 「なぜこの2つが関連づけられたか？」 | Sibling Link Beadのcontent（matched_antigen, relation）を確認 |

### 20.2 法的・規制対応

- **薬機法（SaMD分類）**: チェック結果の信頼性レベルに応じたクラス分類
  - アラート型（逸脱検知のみ）→ クラスI相当
  - 推奨型（用量提案）→ クラスII相当（要検討）
- **医療事故調査**: prescription_check Beadからparentsを辿ることで、判定時の全入力データを暗号学的に証明可能
- **PMDA規制対応**: MedBeadsの不変性により、AIが「何を見て判断したか」の完全な再現が可能

---

## 21. フロントエンド仕様

### 21.1 Timeline View

- 時系列でBead一覧を表示
- リソースタイプ別色分け
- Sibling Link Beadを接続アイコン付きで表示
- 関連するBeadペアを視覚的に結線

### 21.2 Graph View (React Flow)

- DAGのノード・エッジ可視化
- `parent` edge: 実線（縦方向）
- `sibling` edge: 点線（横方向）
- Sibling Link Bead: ダイヤモンド型ノード
- Severity に応じた色分け（info=青, warning=黄, alert=橙, critical=赤）
- Sibling edgeにホバーで relation type と description を表示

### 21.3 Detail Panel

- Bead詳細表示（FHIR content整形）
- `antigens` をタグチップ形式で表示
- Sibling Link Beadの場合、関連先Beadへのリンクと severity バッジ

### 21.4 Viewer Role Selector

- ロール切替によるアクセス制御シミュレーション
- Clearance Badge 表示

---

## 22. 用語集

| 用語 | 定義 |
| --- | --- |
| **Bead** | MedBeadsにおける最小の情報単位。SHA-256ハッシュをIDに持つ不変のデータ粒子 |
| **CAS** | Content Addressable Storage。コンテンツのハッシュ値でアドレスされるストレージ |
| **DAG** | Directed Acyclic Graph（有向非巡回グラフ）。Beadのparents関係が形成する構造 |
| **Antigen** | Beadが持つ検索可能なマーカータグ。namespace体系で管理される表面マーカー |
| **APC Daemon** | Antigen Presenting Cellデーモン。Beadを巡回しantigenマッチでSibling Linkを自動生成 |
| **Sibling Link Bead** | 2つ以上のBeadの横方向の関連を記録する専用Bead。`type="sibling_link"` |
| **暗黙的Sibling** | 同じparentを持つBead同士の関係。動的に算出 |
| **明示的Sibling** | Sibling Link Beadによって記録された関係。`edge_type='sibling'`でインデックス |
| **二次応答** | Sibling Link Bead自身のantigensが他のBeadとマッチし、新たなSibling Linkが生成されること |
| **MCP** | Model Context Protocol。LLMエージェントがMedBeadsを利用するためのインターフェース |
| **Triad Agents** | Decomposer / Navigator / Verifier の3エージェント構成 |
| **Context Bundle** | 1つのBeadから芋づる式に取得された関連Beadの集合（ancestors + descendants + siblings） |
| **drug_master** | 薬剤マスター情報を格納するBead。PMDA添付文書に基づく用量基準・相互作用情報 |
| **Security Clearance** | Beadレベルのアクセス制御。ブラックリストモデルで特定ロールのアクセスを制限 |
| **Daily Summary** | 日次グルーピングノード。同日の記録の共通親として機能し、siblingsを自然に生成 |
