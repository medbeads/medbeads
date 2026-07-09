# MedBeads Sibling Beads 仕様書

Version: 0.1.0-draft | Date: 2026-02-20 | Author: Takahito Nakajima

---

## 1. 概要

本仕様書は、MedBeads (Merkle DAG医療データプラットフォーム) に**Sibling Beads（横方向の関連付け）機能**を追加するための設計を定義する。

### 1.1 目的

現在のMedBeadsは `parents[]` による**縦方向（親子）の関係**のみをDAGエッジとして持つ。しかし医療データには以下のような**横方向の関係**が存在し、これを明示的に表現する仕組みが必要である:

| 横方向の関係 | 例 |
|---|---|
| 薬物相互作用 | ワーファリン処方 ↔ NSAIDs処方（出血リスク増大） |
| 併用禁忌 | MAO阻害剤 ↔ SSRI |
| 処方変更の因果 | 処方A（中止） ↔ 処方B（代替として開始） |
| 検査と処方の関連 | 腎機能検査（eGFR低下） ↔ 用量調整処方 |
| 同一encounter内の並行記録 | 看護記録 ↔ 医師記録（同時刻の異なる視点） |
| 矛盾の検出 | インシデント報告「投与なし」 ↔ EMR記録「投与済み」 |

### 1.2 設計アナロジー: 免疫系モデル

本設計は生物学的な免疫応答をアナロジーとして採用する:

| 免疫系 | MedBeads |
|---|---|
| **細胞膜抗原 (Surface Antigen)** | Beadが持つ `antigens[]` タグ（検索可能なマーカー） |
| **抗原提示細胞 (APC: Antigen Presenting Cell)** | APCデーモン（マッチングエンジン） |
| **免疫応答（抗体結合）** | Sibling Link Beadの生成 |
| **抗体の特異性** | マッチした抗原の種類 = relation type |
| **免疫カスケード** | Sibling Link Bead自身のantigensによる連鎖反応 |

```
┌─────────────┐     ┌─────────────┐
│  Bead (Rx_A) │     │  Bead (Rx_B) │
│             │     │             │
│ antigens:   │     │ antigens:   │
│  - warfarin │     │  - nsaid    │
│  - bleeding │     │  - bleeding │  <-- 共通抗原
│  - CYP2C9   │     │  - COX      │
└──────┬──────┘     └──────┬──────┘
       │    "bleeding"     │
       └───── match! ──────┘
              │
       ┌──────▼──────┐
       │ Sibling Link │  <-- APCデーモンが生成
       │  (APC Bead)  │
       │ relation:    │
       │  drug_inter. │
       │ matched_on:  │
       │  "bleeding"  │
       └──────────────┘
```

---

## 2. 現状の構造（変更前）

### 2.1 Bead型定義

#### Go Core（正規定義）

```go
// medbeads/core/types/bead.go
package types

type Bead struct {
    ID        string                 `json:"id,omitempty"`
    Type      string                 `json:"type"`
    Timestamp string                 `json:"timestamp"`
    Parents   []string               `json:"parents"`
    Content   map[string]interface{} `json:"content"`
}
```

#### TypeScript Frontend

```typescript
// medbeads/ui/src/lib/api.ts
export interface Bead {
  id: string;
  type: string;
  content: any;
  parents: string[];
  timestamp: string;
}
```

#### Python Verification Layer

```python
# medbeads/verification/verification_engine.py
@dataclass
class MedicalBead:
    id: str
    type: str
    timestamp: str
    parents: List[str]
    patient_id: str
    content: Dict[str, Any] = None
    # ... FHIR-specific fields ...
```

### 2.2 SQLiteスキーマ

```sql
-- メタデータテーブル
CREATE TABLE beads (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    parents TEXT,                    -- JSON array: ["hash1", "hash2"]
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    content_text TEXT
);
CREATE INDEX idx_type ON beads(type);
CREATE INDEX idx_timestamp ON beads(timestamp);

-- FTS5全文検索（trigram）
CREATE VIRTUAL TABLE beads_fts USING fts5(
    id UNINDEXED, content, tokenize='trigram'
);

-- エッジテーブル（親子関係）
CREATE TABLE bead_edges (
    child_id  TEXT NOT NULL,
    parent_id TEXT NOT NULL,
    PRIMARY KEY (child_id, parent_id)
);
CREATE INDEX idx_edge_parent ON bead_edges(parent_id);
```

### 2.3 既存のBead Type一覧

#### FHIR系タイプ（Synthea / sample_data由来）

| type | 用途 | content主要フィールド | Parents |
|---|---|---|---|
| `patient_registration` | 患者登録（DAGルート） | `fhir_id`, `gender`, `name` | `[]`（空） |
| `fhir_encounter` | 受診・来院 | `class`, `period`, `reason`, `status`, `subject` | patient_registration |
| `fhir_observation` | 検査・バイタル測定 | `category`, `code`(LOINC), `component[]`, `effectiveDateTime` | patient_registration |
| `fhir_condition` | 病名・診断 | `code`(SNOMED), `clinicalStatus`, `onsetDateTime` | patient_registration |
| `fhir_medicationrequest` | 処方 | `authoredOn`, `dosageInstruction`, `medicationReference` | patient_registration |
| `fhir_medication` | 薬剤情報 | `code`(RxNorm) | patient_registration |
| `fhir_procedure` | 処置・手術 | `code`, `performedPeriod`, `status` | patient_registration |
| `fhir_immunization` | 予防接種 | `vaccineCode`, `date`, `status` | patient_registration |
| `fhir_imagingstudy` | 画像検査 | `modality`, `description`, `started` | patient_registration |
| `fhir_documentreference` | 文書参照 | `type`, `content`, `date` | patient_registration |
| `fhir_diagnosticreport` | 診断レポート | `code`, `result`, `effectiveDateTime` | patient_registration |
| `fhir_goal` | 治療目標 | `description`, `status`, `addresses` | patient_registration |
| `fhir_organization` | 医療機関 | `name`, `type` | patient_registration |

#### EMR-CSV移行タイプ（`migrate_to_medbeads.py` 由来）

| type | 判定条件（日本語record_type / content） |
|---|---|
| `admission` | 「入院」を含む |
| `discharge` | 「退院」を含む |
| `surgery` | 「手術」「手術記録」を含む |
| `anesthesia_record` | 「麻酔」を含む |
| `post_op_note` | 「術後」を含む |
| `pre_op_record` | 「術前」を含む |
| `prescription_update` | 「処方変更」を含む |
| `prescription` | 「処方」「投薬」「抗生剤投与」「解熱剤」「前投薬」 |
| `vital_signs` | 「バイタル」を含む |
| `lab_results` | 「検査」「採血」「血液培養」 |
| `imaging` | 「画像」「CT」「X線」「超音波」 |
| `incident` | 「インシデント」「緊急」「異常」「事故」 |
| `rehabilitation` | 「リハビリ」を含む |
| `health_check` | 「健診」「健康診断」 |
| `consultation` | 「コンサル」を含む |
| `icu_record` | 「ICU」を含む |
| `medical_record` | 上記いずれにも該当しない（デフォルト） |

#### 処方チェック系タイプ（`MedBeads_Prescription_Check_Spec.md` 定義、未実装）

| type | 用途 |
|---|---|
| `drug_master` | 薬剤マスター（用量基準、腎調整、相互作用） |
| `drug_master_source` | 薬剤マスターのデータソース |
| `prescription_order` | 処方オーダー |
| `prescription_check` | 処方チェック結果（AI生成） |
| `dose_adjustment` | 用量調整記録 |
| `interaction_alert` | 相互作用アラート |
| `dispensing_record` | 調剤記録 |

### 2.4 DAGトポロジ（変更前）

```
patient_registration [P-001: 田中一郎]
├── daily_summary [2025-01-10]
│   ├── admission [入院記録]
│   ├── vital_signs [入院時バイタル]
│   ├── lab_results [入院時血液検査]
│   └── prescription [Rx: セファゾリン1g]
├── daily_summary [2025-01-11]
│   ├── surgery [胃切除術]
│   │   ├── anesthesia_record [全身麻酔記録]
│   │   └── post_op_note [術後経過]
│   ├── vital_signs [術後バイタル]
│   └── prescription [Rx: フェンタニル]
└── daily_summary [2025-01-12]
    ├── vital_signs [POD1バイタル]
    └── lab_results [術後血液検査]
```

- 全てのエッジは `parents[]` による**縦方向（親→子）のみ**
- 横方向の関連は暗黙的（`GetSiblings()` で動的に「同じ親の子」を算出）
- 関連の理由・種類は記録されない

### 2.5 既存APIエンドポイント（Go Core Server, Port 8080）

| Method | Endpoint | 説明 |
|---|---|---|
| `POST` | `/beads` | Bead作成 |
| `GET` | `/beads?id=<hash>` | Bead取得 |
| `GET` | `/beads/context?id=<hash>&depth=<n>[&lookup=reverse]` | 祖先/子孫チェーン |
| `GET` | `/beads/siblings?id=<hash>` | 暗黙的sibling（同じ親の子） |
| `GET` | `/patients` | 全患者一覧 |
| `GET` | `/search?q=<text>` | 全文検索 |
| `GET` | `/search/fts?q=<text>&limit=<n>` | FTS5 trigram検索 |
| `GET` | `/search/structured?...` | 構造化検索 |

### 2.6 既存のSibling機能（`store.GetSiblings()`）

現在の `GetSiblings()` は**暗黙的sibling**を動的に算出する:

1. 対象beadの `parents` を取得
2. 各parentの全childrenを取得（`bead_edges`テーブル）
3. 自分自身を除外

**制限事項**:
- 関連の理由が不明（全siblings が等価に扱われる）
- 同じdaily_summaryの子が全て返され、ノイズが多い
- 明示的に作られた関連と区別できない

---

## 3. 設計変更 1: Antigensフィールド

### 3.1 Bead構造の拡張

#### Go Core

```go
// medbeads/core/types/bead.go
package types

type Bead struct {
    ID        string                 `json:"id,omitempty"`
    Type      string                 `json:"type"`
    Timestamp string                 `json:"timestamp"`
    Parents   []string               `json:"parents"`
    Antigens  []string               `json:"antigens,omitempty"` // NEW
    Content   map[string]interface{} `json:"content"`
}
```

#### TypeScript Frontend

```typescript
export interface Bead {
  id: string;
  type: string;
  content: any;
  parents: string[];
  antigens?: string[];  // NEW
  timestamp: string;
}
```

#### Python Verification Layer

```python
@dataclass
class MedicalBead:
    id: str
    type: str
    timestamp: str
    parents: List[str]
    antigens: List[str] = field(default_factory=list)  # NEW
    patient_id: str = ""
    content: Dict[str, Any] = None
```

### 3.2 Antigensの特性

- **Bead作成時に確定** → SHA-256ハッシュに含まれる → CASの不変性を維持
- 1つのBeadが複数のantigensを持てる
- 後から変更不可（変更する場合は新しいBeadを作成）
- APCデーモンがマッチングに使用する**検索可能な表面マーカー**

### 3.3 Antigen語彙定義（FHIR属性ベース）

#### Namespace体系

| namespace | 由来 | 例 | 説明 |
|---|---|---|---|
| `snomed:<code>` | SNOMED CT | `snomed:444814009` | 病名・処置のコード |
| `loinc:<code>` | LOINC | `loinc:55284-4` | 検査項目コード |
| `rxnorm:<code>` | RxNorm | `rxnorm:745679` | 薬剤コード |
| `atc:<code>` | ATC分類 | `atc:B01AA03` | 薬効分類コード |
| `organ:<system>` | 臓器系統 | `organ:renal` | 関連臓器・系統 |
| `risk:<type>` | リスクカテゴリ | `risk:bleeding` | 臨床リスク分類 |
| `actor:<id>` | 関与者 | `actor:tanaka_md` | 記録者・実施者 |
| `temporal:<tag>` | 時間的文脈 | `temporal:pre_op` | 臨床的タイミング |

#### 定義済みantigen値

**organ namespace:**

| antigen | 説明 |
|---|---|
| `organ:renal` | 腎臓・腎機能 |
| `organ:hepatic` | 肝臓・肝機能 |
| `organ:cardiovascular` | 心血管系 |
| `organ:respiratory` | 呼吸器系 |
| `organ:gastrointestinal` | 消化器系 |
| `organ:neurological` | 神経系 |
| `organ:hematologic` | 血液・造血系 |
| `organ:endocrine` | 内分泌系 |
| `organ:musculoskeletal` | 筋骨格系 |

**risk namespace:**

| antigen | 説明 |
|---|---|
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

**temporal namespace:**

| antigen | 説明 |
|---|---|
| `temporal:pre_op` | 術前 |
| `temporal:intra_op` | 術中 |
| `temporal:post_op` | 術後 |
| `temporal:admission` | 入院時 |
| `temporal:discharge` | 退院時 |
| `temporal:emergency` | 緊急時 |

### 3.4 Antigen自動抽出ルール

Bead作成時にcontentおよびtypeから自動的にantigensを付与する。

#### FHIR系Beadからの抽出

| Beadの条件 | 抽出antigen |
|---|---|
| `content.code.coding[].system == "http://snomed.info/sct"` | `snomed:<code>` |
| `content.code.coding[].system == "http://loinc.org"` | `loinc:<code>` |
| `content.code.coding[].system == "http://www.nlm.nih.gov/research/umls/rxnorm"` | `rxnorm:<code>` |
| `content.category[].coding[].code == "vital-signs"` | `organ:cardiovascular`（血圧の場合） |
| `content.code.coding[].display` に腎機能関連語を含む | `organ:renal` |
| `type == "fhir_medicationrequest"` | `risk:*`（薬剤リスクDBから自動付与） |

#### EMR-CSV系Beadからの抽出

| Beadのtype / contentキーワード | 抽出antigen |
|---|---|
| `type == "surgery"` | `temporal:intra_op` |
| `type == "anesthesia_record"` | `temporal:intra_op`, `risk:respiratory_depression` |
| `type == "post_op_note"` | `temporal:post_op` |
| `type == "pre_op_record"` | `temporal:pre_op` |
| `type == "prescription"` + content に「ワーファリン」 | `rxnorm:<code>`, `risk:bleeding`, `organ:hematologic` |
| `type == "lab_results"` + content に「eGFR」 | `loinc:69405-9`, `organ:renal` |
| `type == "vital_signs"` | `organ:cardiovascular` |
| `type == "incident"` | `risk:*`（内容に応じて自動分類） |

### 3.5 Antigenの具体例

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

```json
{
  "type": "lab_results",
  "timestamp": "2025-01-10T10:00:00",
  "parents": ["<daily_summary_hash>"],
  "antigens": [
    "loinc:69405-9",
    "organ:renal",
    "risk:nephrotoxic"
  ],
  "content": {
    "test": "eGFR",
    "value": "45",
    "unit": "mL/min/1.73m2",
    "flag": "L"
  }
}
```

### 3.6 SQLiteスキーマ変更（Antigens用）

```sql
-- antigensインデックステーブル（正規化）
CREATE TABLE bead_antigens (
    bead_id  TEXT NOT NULL,
    antigen  TEXT NOT NULL,
    PRIMARY KEY (bead_id, antigen)
);
CREATE INDEX idx_antigen ON bead_antigens(antigen);

-- beadsテーブルにantigensカラム追加（JSON配列、CAS本体との整合性用）
ALTER TABLE beads ADD COLUMN antigens TEXT DEFAULT '[]';
```

---

## 4. 設計変更 2: Sibling Link Bead

### 4.1 Sibling Link Beadの定義

Sibling関係を記録する**専用のBeadタイプ**。関連する2つ以上のBeadを `parents` として参照し、その関連性のメタデータを `content` に持つ。

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

### 4.2 Sibling Link Beadのフィールド仕様

| フィールド | 型 | 説明 |
|---|---|---|
| `type` | `string` | 常に `"sibling_link"` |
| `timestamp` | `string` | リンク作成日時（ISO 8601） |
| `parents` | `[]string` | 関連付けされるBeadのハッシュID（2つ以上） |
| `antigens` | `[]string` | このリンク自身のantigen（二次応答の対象） |
| `content.matched_antigen` | `string` | マッチのトリガーとなったantigen |
| `content.relation` | `string` | 関係タイプ（下表参照） |
| `content.severity` | `string` | 重要度: `info`, `warning`, `alert`, `critical` |
| `content.description` | `string` | 人間可読な関連性の説明 |
| `content.detected_by` | `string` | 検出者: `apc_daemon`, `manual`, `agent` |
| `content.scan_generation` | `int` | APCデーモンの何周目のスキャンで生成されたか |

### 4.3 Relation Type定義

| relation | 説明 | 例 |
|---|---|---|
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

### 4.4 Severity定義

| severity | 色 | 意味 | 例 |
|---|---|---|---|
| `info` | 青 | 参考情報 | 同時刻の記録の関連付け |
| `warning` | 黄 | 注意喚起 | 腎機能低下と腎排泄薬の併用 |
| `alert` | 橙 | 要確認 | 相互作用の可能性 |
| `critical` | 赤 | 重大 | 禁忌の組み合わせ、矛盾の検出 |

### 4.5 CAS不変性の保証

- Sibling Link Bead自体がCASに保存される → ハッシュが確定する → 改ざん不能
- 元のBead A, B は変更されない（antigens はBead作成時に確定済み）
- 「誰が、いつ、なぜこの2つを関連づけたか」が監査可能なレコードとして残る
- Sibling Link Bead自身もantigensを持つため、**二次応答（リンクのリンク）** が自然に発生しうる

---

## 5. 設計変更 3: bead_edgesテーブル拡張

### 5.1 スキーマ変更

```sql
-- edge_typeカラムを追加
ALTER TABLE bead_edges ADD COLUMN edge_type TEXT DEFAULT 'parent';

-- 複合主キーを更新（edge_typeを含める）
-- NOTE: SQLiteではALTER TABLE でPKを変更できないため、テーブル再作成が必要

CREATE TABLE bead_edges_new (
    child_id   TEXT NOT NULL,
    parent_id  TEXT NOT NULL,
    edge_type  TEXT NOT NULL DEFAULT 'parent',
    PRIMARY KEY (child_id, parent_id, edge_type)
);
CREATE INDEX idx_edge_parent_new ON bead_edges_new(parent_id);
CREATE INDEX idx_edge_type ON bead_edges_new(edge_type);

-- データ移行
INSERT INTO bead_edges_new (child_id, parent_id, edge_type)
SELECT child_id, parent_id, 'parent' FROM bead_edges;

DROP TABLE bead_edges;
ALTER TABLE bead_edges_new RENAME TO bead_edges;
```

### 5.2 Edge Type定義

| edge_type | 説明 | 方向性 |
|---|---|---|
| `parent` | 従来の親子関係（縦方向） | 有向: child → parent |
| `sibling` | Sibling Link Beadによる横方向の関連 | 双方向（両者をedgeとして登録） |

### 5.3 Sibling Edgeの登録ロジック

Sibling Link Beadが作成された時:

1. 通常の `parent` edgeを登録（Sibling Link Bead → 各parent Bead）
2. 追加で `sibling` edgeを登録（parent Bead間の双方向）

```
Sibling Link Bead (SL) の parents: [A, B]

登録されるedge:
  (SL, A, parent)   -- SLの親はA
  (SL, B, parent)   -- SLの親はB
  (A, B, sibling)   -- AとBは横方向で関連
  (B, A, sibling)   -- BとAは横方向で関連（双方向）
```

---

## 6. APCデーモン仕様

### 6.1 概要

APC (Antigen Presenting Cell) デーモンは、MedBeads Coreサーバー内でバックグラウンドに常駐するgoroutineとして動作する。アイドリング中にBeadを巡回し、antigenマッチに基づいてSibling Link Beadを自動生成する。

### 6.2 動作フロー

```
┌──────────────────────────────────────────────────────────┐
│                 APC Daemon (Go goroutine)                  │
│                                                          │
│  loop:                                                   │
│    1. 未スキャンBeadを1つ取得（bead_apc_scanテーブル参照）  │
│    2. そのBeadのantigensを読む                             │
│    3. 親チェーンを遡ってコンテキスト（患者ID等）を取得      │
│    4. 同一antigenを持つ他のBeadを検索                      │
│       （bead_antigensテーブルのインデックスを使用）         │
│    5. 同一患者内のペアに絞り込む                           │
│    6. ペアの臨床的関連性をスコアリング                     │
│    7. スコアが閾値超え → Sibling Link Bead生成            │
│    8. スキャン済みとしてマーク                             │
│    9. sleep(idle_interval)                                │
│  end loop                                                │
│                                                          │
│  ※ 新Bead登録時にも即座にトリガー可能（Event-Driven Mode） │
└──────────────────────────────────────────────────────────┘
```

### 6.3 スキャン管理テーブル

```sql
CREATE TABLE bead_apc_scan (
    bead_id        TEXT PRIMARY KEY,
    scanned_at     TEXT,
    scan_generation INTEGER DEFAULT 0,
    sibling_count  INTEGER DEFAULT 0     -- このBeadから生成されたsibling_link数
);
```

### 6.4 マッチングルール

#### 基本ルール: 同一antigenマッチ

```
IF bead_A.antigens ∩ bead_B.antigens ≠ ∅
AND bead_A.patient == bead_B.patient  (同一患者内)
AND NOT already_linked(bead_A, bead_B)
THEN score = compute_relevance(bead_A, bead_B, matched_antigens)
```

#### スコアリング

| 条件 | スコア加算 |
|---|---|
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
|---|---|
| **生成閾値** | スコア >= 4 で Sibling Link Bead 生成 |
| **重複防止** | 同一ペア × 同一 `matched_antigen` の組は1つだけ |

### 6.5 増殖制御

| 制御パラメータ | デフォルト値 | 説明 |
|---|---|---|
| `max_sibling_depth` | 2 | Sibling Link BeadのLink（二次応答）の最大深度 |
| `max_siblings_per_bead` | 10 | 1つのBeadから生成されるSibling Linkの最大数 |
| `min_score_threshold` | 4 | マッチスコアの最低閾値 |
| `idle_interval` | 5s | スキャン間のスリープ時間 |
| `batch_size` | 10 | 1回のスキャンで処理するBead数 |
| `secondary_response_decay` | 0.5 | 二次応答のスコアに乗算する減衰係数 |

### 6.6 Event-Driven Mode

新しいBeadが `POST /beads` で登録された際、APCデーモンに即座に通知し、待機時間なしでスキャンを開始する。

```go
// apc_daemon.go (概念コード)
func (apc *APCDaemon) OnBeadCreated(beadID string) {
    apc.priorityQueue <- beadID
}
```

---

## 7. DAGトポロジ（変更後）

### 7.1 Sibling Beads込みのDAG

```
patient_registration [P-001: 田中一郎]
├── daily_summary [2025-01-10]
│   ├── admission [入院記録]
│   │   antigens: [temporal:admission, organ:gastrointestinal]
│   ├── vital_signs [入院時バイタル]
│   │   antigens: [organ:cardiovascular, loinc:55284-4]
│   ├── lab_results [入院時血液検査 eGFR=45]
│   │   antigens: [loinc:69405-9, organ:renal, risk:nephrotoxic]
│   └── prescription [Rx: メロペネム 1g]
│       antigens: [rxnorm:855332, risk:nephrotoxic, organ:renal, risk:infection]
│
│   *** Sibling Link Beads (APCデーモンが自動生成) ***
│   └── sibling_link [lab_results ↔ prescription]
│       parents: [<lab_results_hash>, <prescription_hash>]
│       antigens: [risk:nephrotoxic_correlation, alert:dose_adjust]
│       content:
│         matched_antigen: "organ:renal"
│         relation: "clinical_correlation"
│         severity: "warning"
│         description: "メロペネム投与中にeGFR 45 — 用量調整検討"
│
├── daily_summary [2025-01-11]
│   ├── surgery [胃切除術]
│   │   antigens: [snomed:287816003, temporal:intra_op, organ:gastrointestinal]
│   │   ├── anesthesia_record [全身麻酔]
│   │   │   antigens: [temporal:intra_op, risk:respiratory_depression]
│   │   └── post_op_note [術後経過]
│   │       antigens: [temporal:post_op]
│   ├── prescription [Rx: フェンタニル]
│   │   antigens: [rxnorm:4337, risk:respiratory_depression, temporal:post_op]
│   └── vital_signs [術後バイタル SpO2=91%]
│       antigens: [organ:respiratory, loinc:59408-5]
│
│   *** Sibling Link Beads ***
│   ├── sibling_link [anesthesia_record ↔ prescription(フェンタニル)]
│   │   matched_antigen: "risk:respiratory_depression"
│   │   relation: "drug_interaction"
│   │   severity: "alert"
│   │
│   └── sibling_link [prescription(フェンタニル) ↔ vital_signs(SpO2)]
│       matched_antigen: "organ:respiratory"
│       relation: "clinical_correlation"
│       severity: "warning"
│       description: "フェンタニル投与後にSpO2 91%"
│
└── daily_summary [2025-01-12]
    ├── vital_signs [POD1バイタル]
    └── lab_results [術後血液検査]
```

### 7.2 横方向エッジの可視化

```
[lab_results eGFR=45] ◄───sibling───► [Rx メロペネム]
        │                                    │
        └──────── parent ──────────┬─────────┘
                                   ▼
                          [daily_summary 01-10]
                                   │
                                   ▼
                         [patient_registration]
```

- 縦線: `parent` edge（既存）
- 横線: `sibling` edge（新規、Sibling Link Beadが仲介）

---

## 8. API変更

### 8.1 既存エンドポイントの変更

#### `POST /beads`

- Request bodyに `antigens` フィールドを受け付ける
- 作成後、APCデーモンに通知（Event-Driven Mode）

```json
// Request
{
  "type": "prescription",
  "timestamp": "2025-01-10T09:00:00",
  "parents": ["<daily_summary_hash>"],
  "antigens": ["rxnorm:855332", "risk:nephrotoxic", "organ:renal"],
  "content": { "drug": "メロペネム 1g" }
}

// Response
{ "status": "success", "id": "<sha256_hash>" }
```

#### `GET /beads?id=<hash>`

- レスポンスに `antigens` フィールドを含む

#### `GET /beads/siblings?id=<hash>`

- 既存の暗黙的sibling に加え、`edge_type='sibling'` の**明示的sibling**も返す
- レスポンスに `sibling_type` フィールドを追加

```json
// Response
[
  {
    "id": "abc123...",
    "type": "lab_results",
    "sibling_type": "explicit",     // "implicit" or "explicit"
    "relation": "clinical_correlation",
    "severity": "warning",
    "matched_antigen": "organ:renal",
    // ... 通常のBeadフィールド
  }
]
```

### 8.2 新規エンドポイント

#### `GET /beads/sibling-links?id=<hash>`

指定Beadに関連するSibling Link Bead一覧を返す。

```json
// Response
[
  {
    "id": "<sibling_link_hash>",
    "type": "sibling_link",
    "parents": ["<bead_A>", "<bead_B>"],
    "antigens": ["risk:nephrotoxic_correlation"],
    "content": {
      "matched_antigen": "organ:renal",
      "relation": "clinical_correlation",
      "severity": "warning",
      "description": "..."
    }
  }
]
```

#### `GET /antigens/search?antigen=<value>&patient=<id>`

特定のantigenを持つBeadを患者単位で検索。

#### `GET /apc/status`

APCデーモンの状態（スキャン済みBead数、生成済みSibling Link数、キュー長）を返す。

#### `POST /apc/trigger`

APCデーモンの手動トリガー（開発・デバッグ用）。

---

## 9. フロントエンド変更

### 9.1 GraphView（React Flow）

- `sibling` edgeを横方向の点線で描画
- Sibling Link Beadをダイヤモンド型ノードで表示
- severity に応じた色分け（info=青, warning=黄, alert=橙, critical=赤）
- sibling edgeにホバーで関係タイプとdescriptionを表示

### 9.2 Timeline

- Sibling Link Beadをタイムライン上に表示（接続アイコン付き）
- 関連するBeadペアを視覚的に結線

### 9.3 DetailPanel

- 選択したBeadの `antigens` を表示（タグチップ形式）
- Sibling Link Beadの場合、関連先Beadへのリンクと severity バッジを表示

---

## 10. 既存ドキュメントとの関係

### 10.1 MEDBEADS_ARCHITECTURE.md

- **Section 6: DAG Link Specification** に Sibling Link Beadのタイプ定義を追加
- **Section 6.2: Edge Rule** に `sibling_link` の親決定ルールを追加
- **Section 6.4: トラバーサルパターン** に明示的siblingトラバーサルを追加

### 10.2 GRAPHVB_DESIGN.md

- **3つのTraversal戦略 > Sibling Walk** を拡張:
  - 暗黙的sibling（同じparentの子）に加え、明示的sibling（`edge_type='sibling'`）を含める
  - Sibling Link Beadのメタデータ（relation, severity）を活用したフィルタリング

### 10.3 MedBeads_Prescription_Check_Spec.md

- `interaction_alert` タイプは Sibling Link Bead (`relation: "drug_interaction"`) として実装可能
- `prescription_check` の `checks_performed[].evidence_beads` に Sibling Link BeadのIDを含めることで、チェック根拠の追跡が可能

### 10.4 MEDBEADS_CHAIN_RETRIEVAL_DESIGN.md

- **Step 3: 芋づる式トラバーサルの強化** に明示的sibling edgeの辿り方を追加
- `_build_context_bundle()` に sibling edge traversal を統合

---

## 11. 実装ロードマップ

### Phase 1: データモデル変更（Go Core）

1. `types/bead.go` に `Antigens` フィールド追加
2. `store/store.go` に `bead_antigens` テーブル作成 + antigenインデックス
3. `bead_edges` テーブルに `edge_type` カラム追加（マイグレーション）
4. `SaveToCAS()` にantigenインデックス登録ロジック追加
5. `ReindexStorage()` にantigen再インデックスロジック追加

### Phase 2: Sibling Link Bead生成

1. `sibling_link` タイプのBead作成ロジック
2. sibling edge登録ロジック（双方向）
3. `GetSiblings()` の拡張（implicit + explicit sibling統合）
4. 新規エンドポイント追加（`/beads/sibling-links`, `/antigens/search`）

### Phase 3: APCデーモン

1. APCデーモンのgoroutine実装
2. `bead_apc_scan` テーブル + スキャン管理
3. マッチングエンジン（antigen比較 + スコアリング）
4. 増殖制御パラメータ
5. Event-Driven Mode（`POST /beads` からの通知）
6. `/apc/status`, `/apc/trigger` エンドポイント

### Phase 4: Antigen自動抽出

1. FHIR content → antigens 抽出ロジック
2. EMR-CSV content → antigens 抽出ロジック
3. `migrate_to_medbeads.py` の更新（antigen付与対応）
4. 既存sample_dataへのantigenバックフィル

### Phase 5: フロントエンド

1. `Bead` interfaceに `antigens` 追加
2. GraphViewにsibling edge描画（横方向点線）
3. Sibling Link Beadのノード表示（ダイヤモンド型）
4. TimelineにSibling Link表示
5. DetailPanelにantigenタグ + severity バッジ

### Phase 6: 統合テスト

1. case1/case2 データでAPCデーモン動作確認
2. Sibling Link Beadの生成数・適切性の評価
3. 増殖制御の調整（閾値、max depth等）
4. フロントエンドでのsibling可視化確認

---

## 12. 用語集

| 用語 | 定義 |
|---|---|
| **Antigen** | Beadが持つ検索可能なマーカータグ。FHIR属性ベースのnamespace体系で管理される |
| **APC (Antigen Presenting Cell) デーモン** | バックグラウンドでBeadを巡回し、antigenマッチに基づいてSibling Link Beadを自動生成するエンジン |
| **Sibling Link Bead** | 2つ以上のBeadの横方向の関連を記録する専用Beadタイプ。type="sibling_link" |
| **暗黙的Sibling** | 同じparentを持つBead同士の関係。既存の `GetSiblings()` で動的に算出 |
| **明示的Sibling** | Sibling Link Beadによって記録された関係。`edge_type='sibling'` でインデックス |
| **二次応答** | Sibling Link Bead自身のantigensが他のBeadとマッチし、新たなSibling Linkが生成されること |
| **スキャン世代 (scan_generation)** | APCデーモンの巡回回数。二次応答の深度追跡に使用 |
