# U4 状態導出(bead_status + active_views)仕様 — peer 統合確定版

ステータス: **確定(data-reviewer + Codex peer の2独立レビューを統合、2026-07-11)**
親設計: DESIGN_v3.1_draft.md §2、specs/U2_projection_schema.md、specs/U3_link_projector.md。
両ピア判定はいずれも **条件付き GO**。統合ルーブリック: 合意点採用、data-reviewer が発見した2つの
correctness 穴(codex 未指摘)を実証で確認 → 対策を仕様に織り込み。

## ゴール(DESIGN_v3.1 §2)

訂正・承認の状態を投影として決定論導出する: bead_status(active/amended/retracted/unattested)+
active_conditions/active_medications(現行問題・現行処方)。retrieve が これを使う(retracted 除外・
amended 置換・unattested 除外)のは U5。**U4 は導出のみ、read 側結合ゼロ**。

## 合意点(両ピア一致 → 採用)

1. **チェーン解決は Go 側でアルゴリズム実装**。SQL は順序付きスパイン取得 + patient 単位 DELETE+INSERT のみ。
   再帰 CTE + edge 表(=IndexBead hot path 変更 + Reindex 必須)は YAGNI で却下。
2. **status projector は Pod デコード方式**(engine.ListPatientBeads で患者の Bead を読む)。amends/retracts
   edge を index に先行投影しない(hot path を汚さない、Reproject 側の解釈投影として扱う)。
3. **bead_status に patient_root 列を 0007 で追加**(JOIN スコープ却下)。per-patient atomic replace を
   clinical_links と同一パターンに。
4. **active_conditions/medications は物理投影表**(0007)。SQL VIEW 不可(content.clinicalStatus は Pod のみ)、
   read-time 再構築も不可(一覧の増分性・決定論を活かせない)。projector が Pod デコード時に status を投影。
5. **別 projection_name = "record_state_v31"** で新 projector。bead_status + active_* を同一 run・同一患者 tx で
   3表 DELETE+INSERT → manifest フリップ。clinical_links_v31 とは別 lineage(入力集合が違う:
   bead_status は knowledge Bead を参照しない → knowledge_bead_ids=[])。
6. **§2 の3規則を固定順序パイプラインで**: ①retracted マーク → ②attestation ゲート → ③amends 置換。
   順序が非可換なので厳守。
7. **U3 follow-up(loadRule の knowledgeBeadIDs 配線)を U4 に同梱**(別コミット)。rule.go:167 の
   「辞書順最大 ID 勝ち」を「明示集合の中で最大が勝つ」に。2ルール seed テストで固定。

## 実証で確認した correctness 穴2件(data-reviewer 発見、GO ブロッカー)

### 穴1: retraction Bead の患者スコープ漏れ【実証済み・ブロッカー】
- 実証: `resolvePatientRoot`(ingest.go:257-259)は `len(Parents)==0` → `""`(共有 Pod)。retraction Bead が
  `retracts` フィールドだけで対象を指し parents が空だと**共有 Pod に落ち**、ListPatientBeads(patientRoot)
  から外れ、projector が見逃す → 取り消されたはずの記録が active のまま(臨床安全リスク)。
- **対策(仕様に明記)**: **retraction / attestation Bead は対象を `parents[]` にも含めることを要求**する
  (attestation は §2 で既に parents=[対象]、retraction もこれに統一)。ingest 時に retraction 型で
  parents 空を拒否 or 警告。これで patient_root がスコープ内に解決される。
  対策のクロス Pod retraction fixture テストを必須に。

### 穴2: NULL recorded_at + 並び順の軸【実証済み・ブロッカー】
- 実証: `ListPatientBeads` は `ORDER BY b.timestamp, b.id`(read.go:62)= **臨床イベント時刻**順。訂正解決に
  必要なのは **recorded_at(記録時刻)**順。軸が違う(小データのテストを通る subtle bug)。
- **対策**: デコード後に **§2 キーで再ソート**する。順序キーを明示固定(SQLite の NULL デフォルトに依存しない):
  ```sql
  ORDER BY (recorded_at IS NULL) ASC, recorded_at DESC, id DESC   -- 「最新有効」が先。NULL は最古扱い、id 最終 tiebreak
  ```
  Go comparator も同一: 非NULL recorded_at は常に NULL より新しい / 非NULL 同士は recorded_at→id /
  NULL 同士は id のみ。**NULL recorded_at fixture + 遅延臨床 timestamp の amendment fixture** を必須テストに。
  recorded_at は index のみ(ハッシュ外)なので、projector は beads を JOIN して recorded_at を取り、
  デコード content と ID で marry する。

## §2 決定論解決アルゴリズム(患者単位、Go 実装)

入力: 患者内 Bead を `(recorded_at IS NULL, recorded_at DESC, id DESC)` で並べたスパイン + デコードした
type/content/amends/retracts/parents。edge チャネルは3つ: amends[](訂正)/ retracts[](取り消し、
retraction 型も)/ parents[](attestation の対象)。

1. **retracted マーク(最強・最初)**: X ∈ R.retracts な有効 R があれば X は `retracted`
   (retraction_bead_id = 順序キー勝者、current_bead_id=NULL)。以後 X への amends は無効。
   **retraction は attestation ゲート不要**(§2「retracted 最強」と整合。entered-in-error は即時有効、
   content.authorized_by が権限を担保)。※ この方針はユーザー裁定事項として記録。
2. **attestation ゲート**: amends Bead と assessment Bead は attestation 必須。Y ∈ A.parents かつ
   A.content.verdict=='approved' な最新有効 attestation があれば Y は valid、無ければ Y は `unattested`
   (かつ Y は対象を supersede しない = 対象は active のまま)。より新しい verdict=='rejected' は無効化。
3. **amends 置換(valid・非 retracted な amender のみ)**: X の有効 amender の順序キー勝者を辿り、
   chain leaf L(最新有効・非 retracted)を求める。X.status='amended'、superseded_by=直近有効successor、
   current_bead_id=L.id。leaf が後で retracted なら current_bead_id=NULL(retracted への amends 無効を推移的に)。
   元 fact Bead で訂正なしは `active`。

## 0007 マイグレーション(append-only、新規ファイル)

```sql
-- bead_status に patient_root(per-patient atomic replace のため。0006 は不変)
ALTER TABLE bead_status ADD COLUMN patient_root TEXT;
CREATE INDEX idx_bead_status_patient ON bead_status(patient_root, status, bead_id);

-- 現行問題・現行処方の投影表(SQL VIEW 不可のため物理表。projector が Pod content から投影)
CREATE TABLE active_conditions (
  patient_root TEXT NOT NULL, bead_id TEXT NOT NULL, current_bead_id TEXT NOT NULL,
  clinical_status TEXT, verification_status TEXT, projection_run_id TEXT NOT NULL,
  PRIMARY KEY (patient_root, bead_id)
);
CREATE INDEX idx_active_conditions_patient ON active_conditions(patient_root, clinical_status, bead_id);
CREATE TABLE active_medications (
  patient_root TEXT NOT NULL, bead_id TEXT NOT NULL, current_bead_id TEXT NOT NULL,
  medication_status TEXT, intent TEXT, projection_run_id TEXT NOT NULL,
  PRIMARY KEY (patient_root, bead_id)
);
CREATE INDEX idx_active_medications_patient ON active_medications(patient_root, medication_status, bead_id);
```
active 判定 = FHIR content.clinicalStatus=='active' **かつ** bead_status.status='active'(または amended chain の
current leaf)。record_status(bead_status)≠ FHIR clinicalStatus の軸分離を守る(両軸を AND)。
条件/処方は type IN ('fhir_condition','fhir_medicationrequest')。Synthea corpus のトップレベル文字列
content.clinicalStatus を読む。

## projector 構造(record_state projector 新設、internal/engine/projector/)

- `StatusReproject(idx, reader, codeVersion, builtAt)`: building manifest(projection_name="record_state_v31"、
  knowledge_bead_ids=[])→ 患者ごとに ListPatientBeads → §2 解決 → bead_status/active_conditions/
  active_medications を1 tx で DELETE(patient_root スコープ)+INSERT(run_id スタンプ)→ manifest フリップ。
- `insertBuildingManifest`/`flipManifestActive`(reproject.go:349,371)を **projection_name パラメタ化**して再利用
  (現状 ProjectionName ハードコード)。
- clearance で行を消さない(clearance 継承は U5 の read surface で drop)。

## 過剰設計回避(両ピア合意)
- amends/retracts edge 表を「将来の SQL 用」に作らない(YAGNI、Reindex を引く)。
- attestation の scope 絞り込みはまだ実装しない(器だけ先行。verdict=approved なら承認とみなす)。
- active_views は condition/medication 2表で十分(kind 判別1表にこだわらない)。

## must-fix 3点(GO 条件)
1. retraction/attestation の患者スコープ穴を塞ぐ(parents に対象を含める要求 + クロス Pod fixture)。
2. NULL recorded_at + 並び順の軸を明示固定(デコード後 §2 キー再ソート + NULL/遅延 amendment fixture)。
3. 解決の3ステップ順序をテストで固定(未承認 amender は supersede しない / retracted leaf は chain 終端)。
