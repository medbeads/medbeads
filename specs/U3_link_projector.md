# U3 link projector 仕様 — peer 統合確定版

ステータス: **確定(data-reviewer + Codex peer の2独立レビューを統合、2026-07-11)**
親設計: specs/DESIGN_v3.1_draft.md §2/§4/§7、specs/U2_projection_schema.md。両ピア判定はいずれも **条件付き GO**。
統合ルーブリック: 合意点は採用、相違点(crux 1件)は実証で決着(下記)。

## ゴール(DESIGN_v3.1 §4)

リンクを **Bead ではなく純投影 clinical_links 行**として生成する link projector に転換:
- sibling_link Bead 型を廃止(§7 死文化)。リンクは正本にソース Bead を持たない純投影に降格。
- LOINC 同一コード・temporal 単独をトリガー除外(87% ノイズ根絶)。臨床イベント間(risk:/atc:/rxnorm:)に絞る。
- relation/severity をルール Bead から導出。共起は severity=info 固定、warning 以上は curated_knowledge
  (出典 Bead ID 必須、CHECK が DB 強制)。二次応答廃止。
- タグ書込先を bead_antigens → bead_tags に切替。recorded_at を Pod meta WrittenAt から充填。
- 「完全再構築可能」不変条件を強化(下記 crux)。

## 実証で決着した crux: 全再投影の方式 → **patient_root バッチ + manifest フリップ**(単一トランザクションは却下)

- 相違: codex「0006 のまま単一置換で始め実測ゲート」/ data-reviewer「単一 tx 全再投影は 104万規模で NO-GO、
  patient_root バッチ + manifest フリップが GO 条件」。
- 実証(確認済み): `SetMaxOpenConns(1)`・`_busy_timeout=5000`・WAL(index.go:78,100,102)。**現状 reindex は
  既に batchSize=500 で per-transaction 分割**(reindex.go:19,167,194,217)。→ 単一 tx 全再投影は既存設計に逆行。
- 決着: **data-reviewer 案を採用**。単一トランザクション全 DELETE→INSERT は WAL 肥大 + writer ロック長期保持で
  日次 Ingest が5秒タイムアウト → 全停止。既存の per-batch tx 分割方針とも整合する patient_root バッチが正解。
  codex の「実測ゲート」は U3b の done 条件に組み込む(1,135患者・104万で <15分 を実測)。

## 分割(3ユニット、各 builder→reviewer→commit)

### U3a — タグ経路の atomic swap(bead_antigens → bead_tags)+ recorded_at 充填【1原子ユニット】
両ピア一致の最重要制約: **書込と読取6箇所を同一 PR で切り、full reindex まで含める**(片側だけ切ると
空テーブルを読む half-migrated が構造的に発生。migration 0006 ヘッダの警告どおり)。

- 書込: `IndexBead`(write.go:167)を bead_antigens → bead_tags に変更。
- 読取6箇所を bead_tags へ: `GetAntigens`→`GetTags`(read.go:200)/ apc candidateRows(scanner.go:394)/
  frequentAntigens×2(scanner.go:490,503)/ retrieveAnchors(retrieve.go:285)/ matchingAntigens(retrieve.go:435)/
  searchAntigens(tools_read.go:494)。
- recorded_at 充填(同じ IndexBead seam なので同梱): `BeadLocation` に `WrittenAt string` 追加 →
  indexBatch(reindex.go:206)は `rec.Meta.WrittenAt`、Ingest(ingest.go:109)は **`meta := pod.NewMeta(...)` を
  変数化**して `meta.WrittenAt` を loc に流す(Append が WrittenAt を書き換えないことを1テストで pin)→
  beads INSERT(write.go:117)に recorded_at 列追加(ON CONFLICT DO NOTHING = 最初のフレーム優先で正しい)。
- **done means**: `grep -rn "bead_antigens" internal/ --include=*.go`(テスト除く)が **0件**。full reindex 後
  全既存テスト green。recorded_at が充填される(NULL 行が残らない)。旧 bead_antigens 表は残置(append-only、不読)。

### U3b — link projector 新設(clinical_links 生成)
codex+data-reviewer 一致: **scanner 改造ではなく新設**。bead_apc_scan/generation/sibling_link Bead/
engine.Ingest に依存しない。

- 入力: `bead_tags` + `beads` + `_shared` の `link_rule` Bead。
- トリガー除外: LOINC 同一コード・temporal 単独・sibling_link 型・二次応答。臨床イベント間に絞る。
- 出力: `clinical_links` 行を直接書く。**共起は severity=info / evidence_basis=cooccurrence 固定**
  (U3 では共起 info のみ実装、warning ルールは後続)。
- **決定論の担保(両ピア必須)**: `clinical_links.created_at` = 入力 Bead 由来の決定論値(max(a,b) timestamp、
  既存 buildSiblingLinkBead 方式)。`link_id` = `sha256(canonical(bead_a,bead_b,relation,matched_tag,rule_version))`
  の内容導出(乱数/uuid 禁止)。link_rule Bead content の全 map/slice を正準化(既存 joinAntigens 規律)。
- **全再投影エントリポイント**: `Reproject(newKnowledgeBeadIDs, codeVersion)` を新設(`Reindex` とは別関数 —
  Reindex=index.db 消失復旧、Reproject=知識更新時の投影入替、Pod 再スキャンしない)。手順:
  ```
  manifest に status='building' で新 run 作成
  FOR each patient_root(+shared):    # 患者単位トランザクション(数百〜数千行、ms級)
    BEGIN; DELETE 旧 run 行(patient_root スコープ); INSERT 新 run_id で bead_tags/clinical_links; COMMIT
  最後に小トランザクションで manifest フリップ(旧 active→superseded、新 run→active)
  ```
  patient_root スコープなので read(get_links/retrieve は患者スコープ)は1患者内で常に単一 run 一貫。
- **done means**: 1,135患者・104万 Bead で Reproject <15分を実測(codex 実測ゲート)。LOINC 単独/temporal 単独で
  link 生成なしをテスト。sibling_link Bead が新規生成されないことを確認。

### U3c — read サイト切替 + round-trip テスト強化
- MCP: `get_siblings`/`get_sibling_links` → `get_links`(clinical_links 読取、clearance 継承 = 不可視 Bead を
  含む link 全体 drop)。`include_siblings` → `include_links`。retrieve の loadExplicitSiblingEdges 経路も clinical_links へ。
- **round-trip テスト強化(削除でなく置換 — 両ピア必須)**:
  - apc/reindex_roundtrip_test.go を `TestReproject_Deterministic` に置換: 同一 Pod + 同一 manifest で2回
    Reproject → clinical_links/bead_tags が完全一致(projection_run_id は比較除外列に明示)。
  - **異なる knowledge_bead_ids で Reproject すると clinical_links が変わる**ことを1ケースで示す(強化の証明:
    「Pod のみ」では決まらない = manifest 依存の明示)。
  - index/reindex_test.go の TestReindex_MatchesManualIndexBead の比較テーブル配列に **bead_tags/clinical_links を
    追加**(bead_antigens/sibling_pairs を外すだけにしない = カバレッジ緩和を防ぐ)。recorded_at も比較対象に。
  - bead_status は U3 スコープ外(空でよい)。U3 テストは bead_tags/clinical_links に集中。

## U3 着手前にやること(仕様側)
- **DESIGN_v3.1_draft.md の文言修正**: 「投影は Pod のみから完全再構築可能」→「正本事実は Pod のみから再構築可能。
  解釈投影は Pod + knowledge Bead IDs + config_hash + code_version から決定論再構築可能」。両ピア一致の指摘。
- **link_rule Bead content スキーマ確定**(U3b の入力):
  ```json
  { "schema": "medbeads.link_rule.v1", "rule_id": "cooccurrence-risk-atc-v1", "rule_family": "cooccurrence",
    "trigger": { "tag_namespaces": ["atc","risk","rxnorm"], "min_shared": 1, "excludes": {"same_code_namespaces": ["loinc"]} },
    "relation": "clinical_correlation", "severity": "info", "evidence_basis": "cooccurrence",
    "score_model": { "weights": {...正準化必須...} } }
  ```
  clinical_links.rule_version = この link_rule Bead の ID(content hash)。rule_id = 人間可読な安定キー。
  manifest.knowledge_bead_ids = dictionary Bead ID + link_rule Bead ID の JSON 配列。

## failure catalog 起案(data-reviewer、要ユーザー承認)
- 「知識更新のための全再投影を Reindex(Pod 全スキャン)で実装」→ Reindex と Reproject を別関数に分離。
- 「大量データの投影入替を単一トランザクションで原子的に → writer ロック長期保持」→ 原子性の単位を
  patient_root スコープに落とし、グローバル一貫性は manifest active フリップ1点に集約。
