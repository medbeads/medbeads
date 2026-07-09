# MedBeads v3 統合再構築 要件定義書 v2

作成日: 2026-07-09 / 作業ベース: github.com/medbeads/medbeads（main, 5d1f3b9 時点）
v1（漸進改善案）を破棄し、v3 統合再構築として全面改訂。設計詳細は `specs/DESIGN_v3.md` を参照。

## 1. 目的

MedBeads（生成AI用の不変・エージェントネイティブな電子カルテ基盤）を、
**v3 として統合再構築**する。漸進改善ではなく、ストレージ形式・検索・プロセス構成を
根本から再設計し、仕様書 v2.1 の中核機能（antigen / sibling / APC）を実装する。

- **検索速度**: 検索の N+1 クエリと CAS 個別ファイル I/O を構造から排除し、
  1,000患者（≈90万 Bead）で実用速度、1万患者を視野に入れる
- **データ容量**: 患者単位パックファイル + zstd 辞書圧縮で桁削減
- **仕様と実装の乖離解消**: antigen / sibling_link / APC デーモン / 統合検索を実装
- **研究ゴール**: MICCAI 2027（締切 ≈ 2027年2月下旬）に向けた
  RAG vs DAG 比較実験（トークン効率・ハルシネーション率・因果推論の質）を可能にする

### 決定事項（プランニングセッションで合意済み）

- クリーンスレート: 旧データ・Bead ID・保存形式との互換は不要（一方向移行ツールのみ）
- 研究成果 → 実運用の段階発展（M1〜M4）
- 不変条件は維持: 内容ハッシュ = ID、Merkle DAG、Tamper-Evident、append-only、
  インデックスは正本から完全再構築可能

## 2. 利用者とユースケース

- **主利用者**: 医療AIエージェント（MCP 経由の Claude 等）、研究者（開発者本人）
- **UC1**: エージェントが MCP ツール `retrieve` 1回で anchor→semantic 拡張→DAG 走査を
  完結し、トークン予算内で患者文脈バンドルを取得する
- **UC2**: 研究者が UI で患者を検索し、タイムライン・DAG グラフ（sibling エッジ含む）を閲覧する
- **UC3**: 研究者が `bench/` で RAG / FTS / DAG（sibling 無）/ DAG（full）の4アーム比較実験を
  1コマンドで再現実行し、MICCAI 論文の表・図を生成する
- **UC4**: APC デーモンが共通 antigen から sibling_link（薬剤相互作用・禁忌等）を自動生成する

## 3. 画面一覧（React UI、REST 契約凍結により大部分流用）

| 画面 | 変更 |
|---|---|
| 患者検索サイドバー / タイムライン / 詳細パネル | 変更なし（高速化はバックエンド側） |
| DAG グラフビュー | sibling エッジ描画 + antigen チップ表示（論文図版用の最小限、M3） |
| AI インサイトパネル | 廃止（AI 推論は MCP クライアント側へ移行） |

## 4. 機能要件

### R1: ストレージエンジン「Pod」（正本）
- **R1.1** 患者パーティション型 append-only パックファイル。
  `pods/<root先頭2hex>/<root64hex>.pod`（1患者=1ファイル）+ `_shared.pod`。
  フレーム: magic | codec | core_len | meta_len | crc32c | bead_id | core_bytes | meta_bytes
- **R1.2** zstd 共有辞書圧縮（`dict/zstd-v1.dict`、ハッシュ固定）。core_bytes は
  解凍→SHA-256 で自己検証可能であること
- **R1.3** 書き込みプロトコル: Pod append + fsync → SQLite インデックス。
  per-pod `indexed_upto` ウォーターマークでクラッシュ回復
- **R1.4** `medbeadsd verify`（全レコード CRC + 再ハッシュ + DAG 整合）と
  `medbeadsd reindex`（index.db のゼロからの完全再構築）を最初に実装する

### R2: Bead スキーマ v3 / ハッシュ規約
- **R2.1** `ID = sha256(JCS({type, timestamp, author, parents, antigens, content, evidence}))`。
  RFC 8785 (JCS) 準拠。ハッシュ対象外は clearance と signature のみ
- **R2.2** parents / antigens は重複除去 + 辞書順ソート。patient_root は導出情報
  （フレーム meta とインデックス列のみ、ハッシュ対象外）
- **R2.3** v2 のゴールデンハッシュテストを移植し、JCS は RFC テストベクタで単体テスト

### R3: SQLite インデックス（使い捨てキャッシュ）
- **R3.1** `beads(id, patient_root, type, timestamp, pod_id, offset, length)` —
  patient_root は書き込み時に事前解決（N+1 の根治）
- **R3.2** `bead_edges(child_id, parent_id, edge_type)`、`bead_antigens(antigen, bead_id,
  patient_root)`（転置インデックス）、`bead_apc_scan`、clearance 系は v2 踏襲
- **R3.3** FTS5 trigram + external content。生 JSON ではなく種別ごとに平坦化した
  `search_text` を索引し、機械生成の `summary`（1行要約）を保持

### R4: 検索3層 + antigen
- **R4.1** L1 Anchor: FTS5 + 構造化フィルタ → patient_root JOIN 1回で患者集約
- **R4.2** L2 Semantic: sqlite-vec + ruri-v3 埋め込み。Embedder は OpenAI 互換
  `/v1/embeddings` HTTP（既定 llama.cpp、差し替え可）。埋め込みは非同期インデクサ
- **R4.3** L3 Chain: Pod バンドル一括読み + メモリ内 BFS（患者内）、
  再帰 CTE（患者横断の浅いチェーン）
- **R4.4** antigen 抽出は FHIR coding からの決定論的機械抽出のみ
  （snomed:/loinc:/rxnorm: 直接 + 静的辞書で atc:/risk:/organ:）。
  LLM は辞書生成のオフライン補助に限定（インジェストパス禁止）

### R5: APC デーモン（v3.0 はバッチ）
- **R5.1** ingest 後のバッチスキャンで共通 antigen をスコアリングし sibling_link を生成
- **R5.2** 暴走防止5点: 同一ペア×同一抗原 UNIQUE / max_siblings_per_bead=10 /
  二次応答 generation≤2 + スコア減衰 / 高頻度 antigen の IDF フィルタ / 生成レートリミット

### R6: MCP 第一級 API（Go 単一バイナリに統合）
- **R6.1** `medbeadsd` に公式 modelcontextprotocol/go-sdk で MCP サーバーを内蔵
  （stdio + Streamable HTTP）。REST は UI 用の従属 API（engine の薄い投影、現行契約を凍結）
- **R6.2** 統合ツール `retrieve(query, patient_id, antigens, types, semantic, chain_depth,
  token_budget)`: 1往復で anchor→expand→chain。L0/L1/L2 の3粒度でトークン予算に
  貪欲詰め込み、切り捨て分も L2 参照で列挙。provenance 付き
- **R6.3** 読み取り系 ~10 ツール + `rag_search`（純粋ベクトル top-k、実験用）。
  書き込みは system ロール限定
- **R6.4** Python api/（Gemini インサイト）は廃止

### R7: 移行・リポジトリ再編
- **R7.1** 現 main に `v2.2.0` タグ + `v2-maintenance` ブランチで凍結後、main を
  新レイアウト（cmd/ internal/ ui/ bench/ tools/migrate/ specs/）へ破壊的再編
- **R7.2** `tools/migrate`: v2 CAS objects → v3（トポロジカル順、新 ID 再計算、
  旧→新 ID マップ CSV 出力、事後検証付き）の一方向 CLI
- **R7.3** CI を Go / bench(uv) / ui の3ジョブに再編

### R8: MICCAI 実験ハーネス（bench/、uv Python）
- **R8.1** Synthea ingest 時に ground-truth（正解根拠 Bead 集合）を決定論的に同時生成
- **R8.2** Retriever インターフェースで4アーム（rag / fts / dag_nosib / dag_full）を
  同一データ・同一埋め込み・同一 LLM で比較。チャンク = Bead に統一
- **R8.3** メトリクス: トークン効率（API usage 実測）、レイテンシ、recall/precision@budget、
  ハルシネーション率（クレーム分解→帰属判定 = Triad の Decomposer/Verifier 転用）、
  因果順序一致率
- **R8.4** run manifest（git commit + config hash + データセットの Merkle 指紋 + モデル版）で
  再現可能性を暗号学的に固定。`uv run bench run` 1コマンドで全条件再現
- **R8.5** bench/ は MCP/REST 経由でのみ core に触れる（M1 失敗時のフォールバック保険）

## 5. データモデル

設計詳細は `specs/DESIGN_v3.md`。要点: Pod（正本）+ index.db（再構築可能キャッシュ）の
2層。Bead v3 = {type, timestamp, author, parents, antigens, content, evidence}（ハッシュ対象）
+ {clearance, signature}（対象外）。エッジは parent / sibling の2種。

## 6. 外部連携

- **MCP クライアント**: Claude Desktop / Claude Code / bench ハーネス
- **埋め込みサーバー**: llama.cpp（ruri-v3、ローカル既定）または Python サイドカー
- **Gemini 直接連携は廃止**（R6.4）

## 7. 非機能要件

- **PHI**: 実患者情報をコード・テストデータ・ログ・コミットに一切含めない。Synthea のみ
- **性能**（Synthea 1,000患者投入時）: 患者バンドル取得 <10ms、FTS→患者解決 <50ms、
  context bundle p95 <500ms。1万患者は M4 で対応
- **完全性**: verify で全データの暗号学的検証が可能。reindex で index.db を完全再構築可能
- **テスト**: v2 の既存テスト（ゴールデンハッシュ・clearance・graph・cycle）を engine へ
  先に移植してから実装する（「書き直し」ではなく「移植」）。CI 3ジョブ green 維持
- **Python**: uv 必須（pip / poetry / conda 禁止）
- **タイムボックス**: M1 は9月第1週がハードリミット。超過時は
  「現行 v2 core + sibling 増分パッチ（3–4週）」へ機械的にフォールバック

## 8. スコープ外（M4 以降へ送る）

- 処方チェック一式（仕様書 §17、drug_master / PMDA — 第3論文候補）
- Triad Agents の製品化（評価パイプラインとしての転用は R8.3 で実施）
- APC のイベント駆動化・チューニング、EMR-CSV 取込、DID 署名
- Embedded Clearance の UI/監査完備（v3.0 はスキーマ + マスキングのみ）
- 1万患者スケールの最適化、多施設間 DAG マージ、IPFS 連携

## 9. ロードマップと完了基準

| MS | 期限 | 完了基準（要約） |
|---|---|---|
| M1 コア再構築 | 8月末 | ゴールデンハッシュ一致 / reindex 完全再構築 / MCP retrieve 動作 / 1,000患者 ingest + 性能目標 / CI 緑 |
| M2 ハーネス+パイロット | 10月中旬 | 1コマンド再現 / 50患者パイロットで効果確認（Go/No-Go）/ judge 一致率 >85% |
| M3 本実験+論文 | 2027年1月末 | MICCAI 投稿パッケージ + 第三者再現可能 |
| M4 実運用堅牢化 | 投稿後 | 1万患者ベンチ / 処方チェック / 監査クエリ全通過 |

## 10. 役割分担表（go-ts-data チーム）

| 役割 | 担当 |
|---|---|
| go-builder (sonnet) | engine 一式（bead/pod/index/graph/antigen/apc/clearance）、MCP、REST、tools/migrate、bench/ Python（兼務） |
| ts-client-builder (sonnet) | UI の REST 契約追随、GraphView sibling エッジ + antigen チップ（M3） |
| data-reviewer (opus) | Pod フォーマット仕様、SQLite スキーマ、JCS ハッシュ規約、移行の正当性、ベンチ結果の妥当性検証 |
