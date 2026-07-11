# P2 UI 設計 — v3.1 のカルテ体験(design brief 集 + REST 拡張マップ)

ステータス: **設計フェーズ(Stitch 探索完了、実装は P2 で ts-client-builder)**。2026-07-11。
北極星: 「電子カルテ(人が書くメモ)に対抗できる、生成AI のためのカルテ」を UI でも体現する。
UC1〜UC4(批判レビュー 2026-07-10 の4ウォークスルー = 要件 docs/requirements.md の受け入れ基準)に対応。

デザインシステム(Stitch asset `12582191834543603878`): ダークモード主体、情報密度高、Inter/IBM Plex Sans、
8px 角丸、状態を色で控えめに(active=緑、amended=琥珀、retracted=取消線+ミュート、unattested=破線/ghosted、
警告色の赤は真のアラートのみ)、provenance 常時可視。実装は既存 shadcn/Tailwind 規約で(Stitch の HTML は直輸入しない)。

Stitch screen 参照(project `3914435426771524537`):
- UC1 患者サマリー: `3d5d6566283348e99676b236b22e4ad1`
- UC2 退院サマリ作成: `8b11736a576e40f0808113302072738c`
- UC3 当直申し送り: `8c59f8efcba0428497b90377d92d5cb5`
- UC4 インシデント振り返り: `83d28e3bf7f1438890f2b67f1ce8037e`

---

## 現行 REST(v3.1 投影は未返却 = P2 拡張が必要)

現行8ルート: `/patients` `/beads` `/beads/context`(parent エッジのみ)`/search` `/clearance`
`/clearance/check` `/roles` `/resource-counts`。**clinical_links / bead_status / active_conditions /
active_medications / clinical_note の raw_text は一切返していない**。UI 実装前に REST 拡張(下記)が前提。

### P2 REST 拡張マップ(Go 側、engine には投影が既にある)
| 新エンドポイント | 返すもの | engine 側の既存 read |
|---|---|---|
| `GET /patients/{root}/summary` | active_conditions + active_medications + 最新 clinical_note + 各行の bead_status | active_* テーブル / bead_status / clinical_note Bead |
| `GET /beads/{id}/status` | status(active/amended/retracted/unattested)+ current_bead_id + 訂正チェーン | index.DB.BeadStatusFor(U5b で新設済み) |
| `GET /beads/{id}/links` | clinical_links(relation/severity/evidence_basis/rule_version、clearance 継承) | index.DB.GetClinicalLinks(U3c) |
| `GET /patients/{root}/handoff` | severity×鮮度でランクした懸案リスト(全患者横断は別途) | clinical_links severity + recorded_at |
| `GET /beads/{id}/chain?as_of=<t>` | 訂正チェーン + 二重時間軸(timestamp/recorded_at)、as-of フィルタ | amends/retracts + recorded_at |
| `GET /beads/{id}` 拡張 | clinical_note 型の raw_text(順序保存)を content で返す | GetBead(既存) |
- clearance 継承は全経路で維持(U3c/U5b の drop 原則)。UI は Viewer Role で見え方が変わる。
- retrieve の既定挙動(U5b: retracted 除外・amended→current 置換・unattested 除外)を REST summary にも適用。

---

## UC1 患者サマリー(外来再診の直前参照)

- **意図**: 医師が外来再診の直前に「アクティブな問題・現行処方・前回からの変化・医師メモ」を1画面で3分把握する。
  批判レビュー「まず全体像を返す入口がない」への UI 回答。
- **レイアウト**: 左=患者リスト(検索可)/ 中央大=患者データ(縦積み)/ 右レール=provenance。
- **階層(上から)**: ①患者ヘッダー(名前/年齢性別/ID + Viewer Role セレクタ)②Active Problem List
  (active_conditions: 状態ピル。amended=琥珀バッジ+"corrected 日付"、retracted=取消線+ミュート)
  ③Current Medications(active_medications: rxnorm/atc チップ、warning リンクは curated_knowledge を目立たせる)
  ④Physician Notes(clinical_note の raw_text を見出し順序保存の散文で。構造化データと視覚分離)
  ⑤右: Provenance(projection run id、"N 個の不変 Bead から導出、編集不可 — 訂正は新 Bead")。
- **トーン**: 信頼・密・スキャン可能。最初に目に入るべき = 問題リストと現行処方の状態。
- **部品**: Card(shadcn)/ Badge(status ピル)/ code チップ(IBM Plex)/ Sidebar / ロールセレクタ。
- **REST**: `/patients/{root}/summary` + `/beads/{id}/links`。

## UC2 退院サマリ作成(assessment が根拠を parents で固定)

- **意図**: AI が退院サマリを起草 → 医師が attest → 各主張が改ざん不能な根拠 Bead に紐づく。assessment 型の核心。
- **レイアウト**: 左=Admission Course タイムライン / 中央=Draft Summary / 右=Evidence パネル。
- **階層**: ①左: 入院経過(Bead 時系列、異常値=琥珀、retracted=取消線)②中央: **DRAFT—未承認バナー(破線/ghosted)**
  + 散文サマリ + 各主張に上付き証拠マーカー[1][2] + "ATTEST & SIGN" ボタン ③右: 選択中の主張の根拠 Bead
  (id short-hash/type/要約/時刻)"Nothing is asserted without evidence"。
- **トーン**: 「AI が書いたが人が承認、全文が暗号学的に固定された根拠に裏打ち」。未承認=ghosted、承認で solid に。
- **REST**: `/patients/{root}/summary`(入院経過)+ assessment Bead 起草は system role の create_bead + attestation。
- **注**: AI 起草→承認ワークフローの実装は将来機能(器=型は U1/U4 で完了)。UI は起草状態の表示から。

## UC3 当直申し送り(severity×鮮度で優先順位)

- **意図**: 当直医が「今夜何を心配すべきか」を、挿入順でなく臨床的重症度×鮮度でランクした懸案リストで得る。
  批判レビュー「一律 severity=warning がアラート疲労の供給源」への UI 回答。
- **レイアウト**: 上=ツールバー("Sorted by: Severity × Recency")/ 主=優先順カード列 / 右=severity モデル凡例。
- **階層**: カードは高→低優先。CRITICAL(赤枠、稀・curated/guideline 裏付き)→ WARNING(琥珀、curated_knowledge)
  → INFO(ミュート・軽い、co-occurrence)。各カード: 患者/異常値+トレンド矢印/鮮度/severity バッジ/evidence_basis。
  右凡例: 「共起は INFO のみ(統計的・静か)。WARNING+ は出典付き curated 必須。DB が無根拠の高 severity を拒否」。
- **トーン**: 重要な所だけ緊急、それ以外は静か = アラート疲労の逆。**共起=軽い、curated=目立つの視覚差が要**。
- **REST**: `/patients/{root}/handoff`(または全患者横断の handoff エンドポイント)+ clinical_links severity。

## UC4 インシデント振り返り(二重時間軸)

- **意図**: M&M/インシデント調査で「決定の瞬間に記録が実際に何を示していたか」を、訂正履歴(amends/retracts)と
  二重時間軸(イベント時刻 vs 記録時刻)で honest に再構成する。可変 EMR には不可能な MedBeads の核心価値。
- **レイアウト**: 上=As-of 時刻スクラバー(EVENT TIME と RECORD TIME の並列2トラック)/ 中央=訂正チェーン /
  右=Provenance & Audit。
- **階層**: ①上: プレイヘッドを過去時点に。遅延入力の訂正が record time でオフセットして見える ②中央: ある値
  (例 Hb)の Bead 版チェーン。**as-of 時点で見えていた版を "STATE OF KNOWLEDGE AS OF" で強調**、後の訂正は
  ghosted("not yet written at as-of time")、retracted は取消線。amends→ 矢印 ③右: 不変チェーン全体(Bead id/
  author did/イベント時刻/記録時刻/amends・retracts エッジ)"the past is never overwritten, only superseded"。
- **トーン**: forensic・冷静・権威的。**二重時間軸が視覚の主役**。Export Forensic Bundle。
- **REST**: `/beads/{id}/chain?as_of=<t>`(訂正チェーン + bitemporal + as-of フィルタ)。

---

## 実装順(P2、UI は API 語彙確定[U5]済みなので着手可)
1. REST 拡張(上記マップ、Go/ts-client-builder は Go 側担当)= UI の前提。
2. 既存 v2 UI(GraphView/Timeline/DetailPanel)を土台に、UC1 患者サマリーから実装(一番価値が高い入口)。
3. Viewer Role による clearance 表示差(既存 ClearanceEditor/ViewerRoleSelector を活用)。
4. UC2〜UC4 を順次。AI 起草→承認ワークフロー(UC2)の実装は将来。
5. 廃止: AIInsightPanel(旧 Python/Gemini 連携、v3 で廃止済み)。

## 過剰設計回避
- Stitch の生成 HTML は直輸入しない(shadcn/Tailwind 規約と不一致)。design brief を土台に builder が実装。
- 全患者横断 handoff(UC3)や knowledge graph 表示は、まず単一患者スコープで始めて計測後に拡張。
