# U5 API 語彙標準化 + retrieve 既定挙動 + 旧 sibling 除去 — peer 統合確定版

ステータス: **確定(data-reviewer[Claude] + Codex peer の2独立レビューを統合、2026-07-11)**
親設計: DESIGN_v3.1_draft.md §5(API)+ §7(死文化リスト)。両ピア判定はいずれも **条件付き GO**。
統合ルーブリック: 合意点採用、相違 crux 2件を実証/裁定で決着。

## 合意点(両ピア一致 → 採用)

1. **2ユニットに分割**(一括不可 — blast radius が違いすぎる)。
2. **status 正規化を全 read 経路に**(data-reviewer: anchor / items / truncated_refs / anchor_ids の
   **4経路**。clearance が3独立パス持つのと同型)。
3. **順序: status 正規化 → GetBead → clearance**(clearance 判定前に retracted/unattested を返さない。
   ただし amended 置換後・unattested 明示返却時も clearance を必ずかけ直す)。
4. **`BeadStatusFor(ids []string)` バッチ**(N+1 回避、`WHERE bead_id IN (?,...)` 単一クエリ、
   プレースホルダ必須。read.go の PatientRootsFor パターン踏襲。GetClinicalLinks の隣に新設)。
5. **retrieve は active manifest で絞らない**(writePatientState の patient 単位 DELETE により、患者ごとに
   最新 run の行だけが物理存在。retrieve は1患者バンドルなので bead_id 直引きで正しい世代。この前提を
   BeadStatusFor の doc に固定)。
6. **`current_bead_id=NULL`(retracted-chain-leaf)の amended は retracted と同じく drop**。amended→current
   置換は **patient scope 確定の後**(scope 確定前だと first-anchor の患者決定が揺れる)。
7. **graph sibling tier 除去で文脈は痩せる**(explicit tier = sibling_link 由来 + implicit tier = 同一親兄弟。
   後者は sibling_link と無関係な純グラフ近傍だが §7 が「DESIGN_v3 §8 の sibling tier 定義」を死文化対象に
   含むため除去)。clinical_links は「関係情報の sidecar」を代替するが「近傍展開」は代替しない → **痩せを
   仕様として受容**(chainDepth の ancestor/descendant で一部は拾える。bench で U6 検証)。
8. **MCP は clean cut**(deprecation alias 不要 — alias は旧 table 読みを温存し②の除去と両立しない。
   TS/REST への blast-radius はゼロ = 契約差し戻し事由なし)。破壊的である事実を decisions + tool description に明記。
9. **0008 DROP は U5 でやらない・inert 化に留める**(書込読取停止でinert、フル reindex で空に。DROP は後続)。
10. **get_links / retrieveClinicalLinks も status を見る**(clearance だけだと retracted/unattested/
    amended-old な other endpoint をリンクとして返す)。

## 実証で決着した crux 1: 分割順序 → **②除去先行 → ①API/status**

- 相違: codex「①API/status 先行 → ②除去」/ data-reviewer「②除去先行 → ①」。
- 実証(確認済み): retrieve() は retrieve.go:223 で loadExplicitSiblingEdges、:229 で WithSiblings を呼び、
  BuildContext(context.go:213-218)の sibling tier が**非 anchor Bead を item 集合に展開**する。
- 決着: **data-reviewer 案採用**。①を先にやると status フィルタを「sibling tier が展開した item 集合」にも
  配線し、直後の②でその tier ごと剥がす**二重工事**。②を先にやれば retrieve は ancestor/descendant/anchor
  だけの痩せた bundle になり、①は「その痩せた bundle に status 適用」の単純問題になる。

分割:
- **U5a(除去、先行)**: apc パッケージ削除 / cmd/medbeadsd/apc.go 削除 / serve.go・server.go の apc 配線除去 /
  tools_write.go の apc_trigger 除去 / tools_read.go の apc_status・get_siblings・get_sibling_links 除去 /
  write.go の sibling_link 分岐・indexSiblingLink・siblingLinkMatchedAntigens・extractTags 特例除去 /
  graph の Siblings・AddSiblingEdge・WithSiblings・sibling tier 除去 / loadExplicitSiblingEdges 除去。
  **done means**: go vet / go test -tags sqlite_fts5 green、`grep -ri sibling internal/ --include=*.go` が
  apc/graph/mcpserver でヒットゼロ、retrieve は sibling 抜き bundle を返す。**sibling 抜きの reindex
  round-trip 後継テスト**(clinical_links/bead_tags/bead_status が Pod+manifest から再構築)を追加
  (apc/reindex_roundtrip_test.go は apc ごと消えるので invariant が裸にならないよう補填)。
- **U5b(改名 + retrieve 既定挙動)**: retrieveIn.Antigens→Tags / IncludeSiblings→IncludeLinks /
  search_antigens→search_tags / MatchedAntigens JSON キー→matched_tags / read.go に BeadStatusFor 新設 /
  anchor と item/truncated/anchor_ids の4経路に retracted 除外・amended→current 置換・unattested 除外 /
  get_links に relation フィルタ追加。**done means**: reproject 済み fixture で「retracted anchor が
  AnchorIDs に出ない / amended が current_bead_id に置換 / unattested が既定で消え明示フラグで
  not_for_clinical_action ラベル付きで出る」を pin + N+1 なし確認。

## crux 2(ユーザー裁定要): 空 bead_status のフォールバック挙動

- 相違: codex「silent fail-open せず**制御されたエラー**(黙って旧挙動に戻すと retracted を返す危険)」/
  data-reviewer「**absent = active(既定通過)**、さもないと reproject 未実行 store で全件空を返す壊れ方」。
- **統合案(推奨、両立)**: 状況で分ける —
  - **bead_status テーブルが完全に空**(未 reproject、開発初期)→ **absent = active でフォールバック**
    (retrieve が壊れない。record_state projection is missing の警告は出す)。
  - **テーブルに行があるのに特定 ID だけ欠落**(reproject 済みだが不整合)→ **制御されたエラー or
    その ID を除外**(異常を黙って通さない)。
- これは臨床安全 vs 開発実用性の設計判断なので**ユーザー裁定**を仰ぐ(下記)。

## include_links の意味(過剰設計回避、両ピア指摘)
- `include_links` を「旧 include_siblings の単なる改名」にしない。「リンク**情報**を返す(sidecar)」のか
  「リンク先 Bead も**文脈に入れる**(近傍展開)」のかを明確に分ける。**U5 では sidecar に留める**
  (clinical_links フィールドで other endpoint の関係情報を返すが、context item には展開しない)。
  近傍展開は projection-link expansion として将来別設計(graph.Bundle rewire を避ける)。

**後続更新（2026-07-13）**: 上記の将来ユニットを R9 として実装した。`include_links=true` は
sidecar に加えて、状態・clearance 適用済みの患者内リンク先を bounded BFS で context item に展開する。
契約と安全上限は `specs/R9_projection_link_expansion.md` を正とする。

## failure catalog 起案(data-reviewer、要ユーザー承認)
- 「複数の独立フィルタパス(clearance が3経路)を持つ read ハンドラに新軸フィルタ(status)を足すとき、
  一部パスにだけ適用して truncated_refs/anchor_ids を取りこぼす」→ symptom: 本体からは消えたレコードが
  truncated/anchor 一覧に残る / wrong instinct: item 集合だけにフィルタ / correct move: 既存フィルタ経路数を
  grep で数え上げ、新軸を全経路に同数適用してからレビュー。

## must-fix 3点(GO 条件、両ピア統合)
1. amended→current 置換は patient scope 確定後 + current=NULL の amended は drop(fixture で pin)。
2. status フィルタを4経路すべてに + BeadStatusFor バッチ(プレースホルダ、N+1 回避)。
3. 空 bead_status フォールバック(上記 crux 2 の裁定に従い実装 + 明示テスト)。
