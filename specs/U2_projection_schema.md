# U2 投影スキーマ仕様(migration 0006)— peer 統合確定版

ステータス: **確定(data-reviewer + Codex peer の2独立レビューを統合、2026-07-11)**
親設計: specs/DESIGN_v3.1_draft.md §2/§3/§6。両ピア判定はいずれも **条件付き GO**。
統合ルーブリック適用: 合意点は採用、相違点(crux 2件)は実証で決着(下記「実証で決着した crux」)。

## スコープの確定(half-baked 回避)

U2 = **スキーマの器の新設 + `beads.recorded_at` 追加まで**。実データの書き込み経路切替は U3/U4。
理由(両ピア統合): 0006 で新表を作るだけなら新表は空・旧 `bead_antigens`/`sibling_pairs` 経路は
無傷なので、**中間半端状態が構造的に発生しない**。書き込み切替とフル reindex は U3 に集約する
(codex Q2「二重書き禁止・フル reindex 1操作に集約」+ data-reviewer「0006 と projector 最小実装の
着地順序」を両立)。

U2 の done 条件:
- migration 0006 適用が冪等・`SchemaVersion`==6
- 既存テスト green(`CGO_ENABLED=1 go test -tags sqlite_fts5 ./... -race`)—
  **既存経路(bead_antigens/sibling_pairs)は一切変更しない**ので既存テストは無改変で通るはず
- 新表の CREATE と索引が下記 DDL と一致、reviewer 承認 → checkpoint commit

## 実証で決着した crux(記録)

### crux 1: `beads.recorded_at` を 0006 で追加すべきか → **YES(追加する)**
- 実証: `beads` 表(0001)に recorded_at 列は**無い**(timestamp=イベント時刻のみ)。Pod フレーム meta には
  `WrittenAt`(pod/record.go:51、RFC3339Nano、書き込み時刻)が**既に存在**するが投影に載っていない。
- §2 の訂正チェーン解決は `recorded_at → bead ID 辞書順`で最新有効を選ぶため、recorded_at が投影に
  無いと bead_status を SQL で決定論導出できない。→ codex must-fix #1 を採用。
- 対応: 0006 で `ALTER TABLE beads ADD COLUMN recorded_at TEXT`。**充填は U3 のフル reindex 時**に
  `IndexBead` が Pod meta の WrittenAt を書く(0006 時点では既存行 NULL のまま。U4 status projector は
  NULL を拒否)。

### crux 2: clinical_links の「完全再構築可能」不変条件 → **不変条件を再定義(強化)する**
- 実証: reindex_roundtrip_test.go は sibling リンクが **sibling_link Bead(正本)**として Pod に
  落ちているから「Pod のみから再構築」を検証できている。§7 で sibling_link Bead 型を廃止し
  clinical_links(正本ソース無し)に降格すると、再構築入力が「Pod のみ」→「Pod + 知識 Bead +
  projector コード版」に変わる。
- 決着(両ピア補完): この変化は §0 が既に認めた「決定論再構築は入力の凍結保存とセットで成立」の
  具体化。**U3/U4 の done 条件**として、round-trip テストを「同一 Pod + 同一 manifest(=知識 Bead ID
  集合 + config_hash 固定)→ 同一 clinical_links/bead_tags」の決定論テストへ**強化(緩和ではない)**する。
  U2 ではまだ clinical_links を埋めないので既存 round-trip テストは無傷。

## 確定 DDL(0006_projection_v31.sql、append-only・1トランザクション)

```sql
-- crux 1: 訂正チェーン解決に必須の記録時刻。Pod meta WrittenAt を U3 reindex で充填。
ALTER TABLE beads ADD COLUMN recorded_at TEXT;
CREATE INDEX idx_beads_root_recorded ON beads(patient_root, recorded_at, id);

-- bead_antigens の意味的後継。0005 の3索引を漏れなく引き継ぐ(欠けると ~200x full-scan 回帰)。
-- FK を beads(id) に張らない(投影は導出物。FK は置換 DELETE の順序制約を生む。sibling_pairs の先例に倣う)。
CREATE TABLE bead_tags (
  tag               TEXT NOT NULL,
  bead_id           TEXT NOT NULL,
  patient_root      TEXT,                 -- NULL = shared(beads 系の規約に合わせる。vec0 の '' 特例は伝播させない)
  projection_run_id TEXT,                 -- 行レベル provenance(世代共存ではなく混在検出・原子切替のため)
  PRIMARY KEY (tag, bead_id)              -- 転置索引(0001 bead_antigens PK 踏襲)
);
CREATE INDEX idx_bead_tags_patient ON bead_tags(patient_root, tag, bead_id);  -- 0005 の covering 索引
CREATE INDEX idx_bead_tags_bead    ON bead_tags(bead_id, tag);                -- GetTags(旧 GetAntigens)
CREATE INDEX idx_bead_tags_run     ON bead_tags(projection_run_id);

-- sibling_pairs + bead_edges('sibling') の後継。matched_tag を独立列に(監査クエリのため)。
-- 一意性は (bead_a,bead_b,relation,matched_tag) が担う(旧 sibling_pairs UNIQUE の後継)。link_id は表示 ID。
CREATE TABLE clinical_links (
  link_id           TEXT NOT NULL,
  bead_a            TEXT NOT NULL,        -- bead_a < bead_b 正規化
  bead_b            TEXT NOT NULL,
  patient_root      TEXT NOT NULL,        -- 患者内リンクのみ(shared 禁止)
  relation          TEXT NOT NULL,
  matched_tag       TEXT NOT NULL,        -- 共起の根拠タグ(旧 matched_antigen 相当・監査で索引する)
  severity          TEXT NOT NULL CHECK (severity IN ('info','warning','alert','critical')),
  evidence_basis    TEXT NOT NULL CHECK (evidence_basis IN ('cooccurrence','curated_knowledge','guideline')),
  evidence_bead_ids TEXT NOT NULL DEFAULT '[]',  -- 正準 JSON 配列: ルール/ガイドライン/辞書/出典 Bead
  score_breakdown   TEXT NOT NULL DEFAULT '{}',  -- 正準 JSON(フィルタ/JOIN しない)
  rule_id           TEXT,
  rule_version      TEXT,                 -- = ルール Bead ID(知識世代)
  projection_run_id TEXT,
  created_at        TEXT NOT NULL,
  CHECK (bead_a < bead_b),
  -- 共起は info 固定、warning 以上は curated_knowledge/guideline + 出典必須(§4 の暴走防止)
  CHECK (severity = 'info'
         OR (evidence_basis IN ('curated_knowledge','guideline')
             AND rule_version IS NOT NULL AND evidence_bead_ids <> '[]')),
  UNIQUE (bead_a, bead_b, relation, matched_tag)
);
CREATE INDEX idx_clinical_links_a               ON clinical_links(bead_a);
CREATE INDEX idx_clinical_links_b               ON clinical_links(bead_b);
CREATE INDEX idx_clinical_links_patient_sev     ON clinical_links(patient_root, severity, relation);
CREATE INDEX idx_clinical_links_run             ON clinical_links(projection_run_id);

-- 訂正チェーン解決の決定論導出(§2)。FHIR clinicalStatus とは別概念。current_bead_id で最新版へ即置換。
CREATE TABLE bead_status (
  bead_id             TEXT PRIMARY KEY,
  status              TEXT NOT NULL CHECK (status IN ('active','amended','retracted','unattested')),
  current_bead_id     TEXT,               -- 最新有効版(retrieve の amended→最新置換に使う)
  superseded_by       TEXT,
  attestation_bead_id TEXT,
  retraction_bead_id  TEXT,
  reason              TEXT,
  projection_run_id   TEXT
);
CREATE INDEX idx_bead_status_active  ON bead_status(status, bead_id);
CREATE INDEX idx_bead_status_current ON bead_status(current_bead_id);

-- 追記専用の世代台帳。retrieve provenance に projection_run_id を載せる根拠。
-- 参照した全知識 Bead ID(辞書+ルール)を保持し監査の連鎖を切らさない。
CREATE TABLE projection_manifest (
  run_id            TEXT PRIMARY KEY,
  projection_name   TEXT NOT NULL,
  code_version      TEXT NOT NULL,        -- git SHA + ビルドタグ/アルゴリズム版を config_hash に畳む
  knowledge_bead_ids TEXT NOT NULL DEFAULT '[]',  -- 正準 JSON: dictionary + link_rule Bead ID 集合
  config_hash       TEXT NOT NULL,
  input_watermarks  TEXT NOT NULL,        -- JSON: pod path -> indexed_upto(増分再現に必須)
  built_at          TEXT NOT NULL,
  activated_at      TEXT,
  superseded_at     TEXT,
  status            TEXT NOT NULL CHECK (status IN ('building','active','superseded','failed')),
  CHECK (activated_at IS NULL OR status IN ('active','superseded'))
);
-- projection_name ごとに active は最大1つ(部分一意索引)
CREATE UNIQUE INDEX idx_projection_manifest_one_active
  ON projection_manifest(projection_name) WHERE status = 'active';
```

## 意図的に U2 で作らないもの(過剰設計回避 — 両ピア合意)

- **active_conditions / active_medications は物理表を作らない**。まず VIEW/再構築専用で U4 に持ち込み、
  計測で遅ければ物理化(data-reviewer Q5-4 + codex Q5-2)。0006 で確定させるのは早すぎる。
- 実データ表への **beads(id) FK なし**(FK=ON 環境で置換 DELETE の順序制約を避ける)。

## U3 以降へ引き継ぐ done 条件(このレビューで確定した制約)

- U3: 書込先を bead_antigens→bead_tags に切替 + 全読取箇所(read.go / apc/scanner.go / mcpserver 2箇所)
  を一斉切替 + フル reindex。**二重書き禁止**。recorded_at を IndexBead が Pod meta WrittenAt から充填。
- U3/U4: round-trip テストを「同一 Pod + 同一 manifest → 同一 clinical_links/bead_tags」の決定論テストへ
  **強化**(緩和ではない)。link projector と projection_run_id 生成者を着地。
- 全再投影=1トランザクション原子置換の WAL サイズ/ロック保持時間は **104万規模で未計測** → U3 で実測、
  長すぎれば patient_root バッチ + manifest activated_at フリップに退避。
```
