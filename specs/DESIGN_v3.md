# MedBeads v3 設計書（DESIGN_v3）

作成日: 2026-07-09 / ステータス: 承認済み（プランニングセッション）
要件は `docs/requirements.md` v2、正規仕様は MEDBEADS_SPECIFICATION_v2.1（specs/ へ収載予定）を参照。

## 1. 背景と決定事項

現行実装（v2.x）の構造的課題: 検索時の N+1 クエリ（findPatientRoot の1ノード1クエリ BFS）、
CAS の1 Bead = 1ファイルによる個別 I/O、仕様書 v2.1 中核機能（antigen / sibling / APC /
Vector Search）の未実装。漸進改善ではなく v3 として統合再構築する。

- クリーンスレート（旧データ・Bead ID 互換不要、一方向移行ツールのみ）
- 規模: 1,000患者（≈90万 Bead）快適、1万患者視野
- ゴール: MICCAI 2027（RAG vs DAG、トークン効率、ハルシネーション率）→ 実運用
- 不変条件: 内容ハッシュ=ID、Merkle DAG、Tamper-Evident、append-only、
  インデックスは正本から完全再構築可能

## 2. プロセス構成

```
medbeadsd（Go 単一バイナリ・唯一の常駐プロセス）
├ internal/engine/   ← 唯一の真実の API（Go パッケージ）
│  ├ bead/      型・RFC 8785 (JCS) 正準化・ハッシュ・Verify
│  ├ pod/       Pod パックファイル Writer/Reader/Scanner/CRC
│  ├ index/     SQLite（FTS5 trigram・edges・antigens・apc_scan）+ Reindex
│  ├ graph/     患者バンドルのメモリ展開・BFS・context bundle
│  ├ antigen/   決定論的抽出ルール + 静的マッピング辞書
│  ├ apc/       APC スキャナ（v3.0 はバッチ、イベント駆動は M4）
│  └ clearance/ embedded + DB ルール・監査ログ（v2 から移植）
├ internal/mcpserver/  ← 第一級 I/F（公式 modelcontextprotocol/go-sdk、stdio + HTTP）
└ internal/rest/       ← UI 用の従属 API（engine の薄い投影、現行契約を凍結）

bench/（Python, uv、常駐しない）… Synthea 取込・RAG ベースライン・MICCAI 評価
ui/（React、REST 契約凍結により大部分流用）
```

- ガバナンス: 新機能はまず engine 関数 + MCP ツールとして着地し、UI が必要とするものだけ
  REST に投影する
- Python api/（Gemini インサイト）は廃止。AI 推論は MCP クライアント側の仕事
- Go MCP SDK に問題が出た場合の退避経路: REST 完備 + FastMCP プロキシ復活（1日作業）

## 3. ストレージ: 患者パーティション型 append-only パック「Pod」

```
medbeads_data/
├ pods/<root先頭2hex>/<root64hex>.pod  ← 正本。1患者=1ファイル、append-only
├ pods/_shared.pod                     ← 患者に属さない Bead（drug_master 等）
├ dict/zstd-v1.dict                    ← zstd 共有辞書（ハッシュ固定）
└ index.db                             ← SQLite（正本から完全再構築可能）
```

- フレーム: `magic u16 | flags u8 (codec: raw/zstd/zstd-dict) | core_len u32 | meta_len u32 |
  crc32c u32 | bead_id [32]byte | core_bytes (JCS 正準 JSON, zstd-dict 圧縮) | meta_bytes`
- core_bytes を解凍→SHA-256 すると bead_id に一致（自己検証可能）
- 1患者のサブグラフ（〜900 Bead ≈ 圧縮後 300–500KB）を 1 open + 1 順次読みで取得。
  これが検索・タイムライン桁改善の本体（現行: 900回の open/read/gunzip）
- append-only・削除なしのため GC/compaction 不要。破損は CRC + tail-truncate 回復
- 書き込み: Pod append + fsync → SQLite インデックス（1トランザクション）。
  per-pod `indexed_upto` ウォーターマークでクラッシュ時は Pod 末尾を追走再インデックス。
  「正本が常に先、インデックスは追いつける」の一方向依存に固定
- 並行性: Pod ごとに単一ライター（mutex）。ローカルファースト用途には十分
- patient_root の事前解決: 新規 Bead の parents の patient_root を index から1回引いて継承。
  patient_registration 自身が root。parents なし / 複数 root → `_shared`
- **設計裁定**: 「SQLite に content BLOB を持つ」案は本文二重保持が再発するため不採用。
  患者スコープの読みは Pod バンドル→メモリ内隣接リスト BFS を正とし、
  患者横断の浅いチェーン（drug_master 改訂等）のみ bead_edges への再帰 CTE を使う
- 圧縮: zstd + 共有辞書（klauspost/compress、pure Go）。辞書は移行時に実データから
  トレーニングし、コーデック ID をフレームに記録（将来の辞書更新に対応）

## 4. Bead スキーマ v3 / ハッシュ規約

```
ID = sha256( JCS({ type, timestamp, author, parents, antigens, content, evidence }) )
```

- CanonicalJSON は RFC 8785 (JCS) に準拠と明文化（数値の ES6 形式シリアライズまで規定。
  検査値 float のハッシュ割れ事故を防ぐ）。実装は gowebpki/jcs 等
- v2.1 §4.3 からの変更: author・evidence をハッシュ対象に含める（Tamper-Evident の一貫性）。
  ハッシュ外は本質的に可変な clearance と、ID を署名対象とする signature のみ
- parents・antigens は重複除去 + 辞書順ソート（順序非依存 = 同一内容同一 ID）
- patient_root は導出情報（フレーム meta とインデックス列のみ、ハッシュ対象外）
- ID 表記: 内部は素の 64 hex、API/表示層でのみ `sha256:` プレフィックス
- v2 のゴールデンハッシュテストを移植。JCS は RFC テストベクタで単体テスト（v3 最重要テスト）

## 5. SQLite スキーマ（index.db）

```sql
CREATE TABLE pods (
  pod_id INTEGER PRIMARY KEY, path TEXT UNIQUE, patient_root TEXT,
  size INTEGER, indexed_upto INTEGER   -- クラッシュ回復用ウォーターマーク
);
CREATE TABLE beads (
  id TEXT PRIMARY KEY,
  patient_root TEXT,                    -- 書き込み時に事前解決。NULL = shared
  type TEXT NOT NULL, timestamp TEXT NOT NULL,
  pod_id INTEGER NOT NULL, offset INTEGER NOT NULL, length INTEGER NOT NULL,
  summary TEXT                          -- 機械生成の1行要約（トークン予算 L1 用）
);
CREATE INDEX idx_beads_root ON beads(patient_root, timestamp);
CREATE INDEX idx_beads_type ON beads(type);

CREATE TABLE bead_edges (
  child_id TEXT NOT NULL, parent_id TEXT NOT NULL,
  edge_type TEXT NOT NULL DEFAULT 'parent',   -- 'parent' | 'sibling'
  PRIMARY KEY (child_id, parent_id, edge_type)
);
CREATE INDEX idx_edge_parent ON bead_edges(parent_id, edge_type);

CREATE TABLE bead_antigens (
  antigen TEXT NOT NULL, bead_id TEXT NOT NULL, patient_root TEXT,
  PRIMARY KEY (antigen, bead_id)              -- antigen 先頭 = 転置インデックス
);

CREATE VIRTUAL TABLE beads_fts USING fts5(id UNINDEXED, search_text,
  tokenize='trigram', content='');            -- external content、平坦化テキストを索引
-- bead_apc_scan / clearance_rules / clearance_audit は v2 踏襲
-- sqlite-vec の vec0 仮想テーブル（埋め込み、patient_root を partition key に）
```

- 検索の患者解決: FTS ヒット → beads.patient_root を IN 1クエリで解決（findPatientRoot 廃止）
- search_text: 生 JSON ではなく Bead 種別ごとに人間可読テキストへ平坦化
  （例: fhir_medicationrequest → 「メロペネム 1g 点滴静注 8時間毎」）

## 6. 検索アーキテクチャ（3層 + antigen）

- **L1 Anchor**: FTS5 trigram + 構造化フィルタ（type / timestamp / patient_root の複合索引に
  プッシュダウン）→ JOIN 1回で患者集約
- **L2 Semantic**: sqlite-vec（同一 DB、patient_root で pre-filtering ネイティブ対応）+
  ruri-v3 (cl-nagoya/ruri-v3-310m) 埋め込み。Embedder は OpenAI 互換 `/v1/embeddings` HTTP
  インターフェースに統一（既定: llama.cpp サイドカー。GPU サーバーでは vLLM /
  sentence-transformers に設定1行で差し替え可）。埋め込み生成は書き込みパスから分離した
  非同期インデクサ（bead_embed_queue + goroutine）— 埋め込みサーバー停止時も ingest は止まらない
- **L3 Chain**: 患者内は Pod バンドル + メモリ BFS（siblings 展開・descendants が map 操作）、
  患者横断は再帰 CTE
- **antigen 抽出**: FHIR coding から決定論的に機械抽出。content.code.coding[] の system URI →
  snomed:/loinc:/rxnorm: 直接抽出、静的マッピング辞書（バージョン管理された埋め込み JSON）
  経由で atc:/risk:/organ:、type ベースルールで temporal:。
  **LLM は辞書生成のオフライン補助のみ**（人手レビュー→辞書コミット→決定論的適用）。
  インジェストパスに LLM を入れない — ID の決定論性と研究再現性を守る

## 7. APC デーモン（sibling_link 自動生成）

v3.0 は ingest 後のバッチスキャン（常駐イベント駆動は M4）。スコアリングは仕様書 §9.3 準拠、
マッチ候補は bead_antigens を (antigen, patient_root) で引くため患者内で完結。

暴走防止5点:
1. sibling_pairs(bead_a, bead_b, matched_antigen) UNIQUE 制約
2. max_siblings_per_bead = 10（bead_apc_scan.sibling_count で強制）
3. 二次応答（sibling_link の sibling_link）は generation ≤ 2 + スコア 0.5^generation 減衰
4. 患者内出現率が閾値超（例 30%）の antigen はトリガーから除外（IDF フィルタ、バイタル系の
   組合せ爆発防止）
5. sibling_link 生成レートリミット（患者あたり/スキャン周期あたり上限）

インクリメンタル: bead_apc_scan をウォーターマークに「新 Bead vs 患者内スキャン済み」のみ照合。
辞書改訂時のみ患者単位再スキャン API。

## 8. エージェント向け統合 API（MICCAI 実験の主役）

MCP ツール `retrieve(query, patient_id, antigens, types, date_range, semantic, chain_depth,
token_budget)` → engine の `/context-bundle` 1往復で anchor→expand→chain を完結。

- トークン予算: 各 Bead に3粒度 — L0 = content 全文（〜500 tok）/ L1 = 1行要約（〜40 tok、
  beads.summary）/ L2 = ID + type + timestamp（〜15 tok）。優先度順（anchor L0 →
  sibling_link description → 祖先 L1 → explicit siblings L1 → implicit siblings L2 →
  子孫 L2）に予算まで貪欲詰め込み。**切り捨て分も L2 参照で必ず列挙**し、エージェントが
  get_bead で追加取得できる
- provenance（FTS スコア / ベクトル類似度 / matched_antigen）を含め監査と実験ログを兼ねる
- ほかツール: list_patients / search_beads / get_bead / get_context / get_timeline /
  get_siblings / get_sibling_links / search_antigens / verify_integrity / apc_status /
  apc_trigger + `rag_search`（純粋ベクトル top-k、比較実験用）。
  書き込み（create_bead）は system ロール限定

## 9. bench/（MICCAI 実験ハーネス、uv Python）

```
bench/
├ ingest/     Synthea FHIR → Bead（edge rule + antigen 抽出）。★ ground-truth 同時生成
├ scenarios/  臨床質問 YAML（患者ID・質問・正解・根拠BeadID群・カテゴリ・推論型）
├ retrieval/  Retriever IF 実装: rag.py / fts.py / dag_nosib.py / dag.py の4アーム
├ llm/        Claude/Gemini 共通クライアント（温度・seed 固定、全往復 JSONL 記録）
├ metrics/    トークン効率・レイテンシ・recall/precision@budget・ハルシネーション率
│             （クレーム分解→帰属判定 = Triad の Decomposer/Verifier 転用。
│              コンテキスト外/全記録内 = 検索失敗、全記録外 = ハルシネーションを分離計上）
│             ・因果順序一致率
└ runs/       run manifest = {git commit, config hash, データセット Merkle 指紋, model version}
```

- チャンク = Bead に統一（両方式で検索単位を揃える — 比較の公平性の要）
- ground-truth は Synthea の生成情報から ingest 時に決定論的に生成（臨床医評価はサンプル検証）
- **bench/ は MCP/REST 経由でのみ core に触れる** — M1 失敗時に「v2 core + sibling 増分
  パッチ」へ差し替えても M2/M3 がそのまま進む保険

## 10. リポジトリ戦略・ロードマップ・リスク

- モノレポ維持・main を破壊的再編（論文が引用する URL を守る）:
  ① `v2.2.0` タグ + `v2-maintenance` ブランチで凍結 → ② main 新レイアウト
  （cmd/ internal/ ui/ bench/ tools/migrate/ specs/）→ ③ README に v2 案内。
  CI は Go / bench(uv) / ui の3ジョブ
- tools/migrate: v2 objects 全読み → Kahn 法トポロジカル順 → 新 ID 再計算 →
  旧→新 ID マップ CSV → Pod へ append → 事後検証（件数・再ハッシュ・エッジ・FTS）
- M1 コア再構築（〜8月末、ハードリミット9月第1週）→ M2 ハーネス+パイロット
  （〜10月中旬、Go/No-Go ゲート）→ M3 本実験+論文（〜2027年1月末）→ M4 実運用堅牢化。
  完了基準は docs/requirements.md §9
- M1 は「書き直し」ではなく「移植」: v2 の既存テストを先に engine へ移してから実装。
  実装順: bead → pod+verify → index+reindex → store → graph → antigen → バッチ APC →
  clearance 移植 → MCP/REST
- 性能目標（Synthea 1,000患者）: 患者バンドル取得 <10ms、FTS→患者解決 <50ms、
  context bundle p95 <500ms
- 既知リスク: 自作 Pod フォーマット（→ verify/reindex 先行実装・フォーマットを増長させない）、
  JCS 数値正規化（→ RFC テストベクタ）、ruri-v3 の llama.cpp 対応（→ Embedder IF で
  Python サイドカーに即時フォールバック）、Go MCP SDK（→ FastMCP プロキシ退避経路）
