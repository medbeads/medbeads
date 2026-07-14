# R9 projection-link expansion 仕様

ステータス: **実装済み（2026-07-13）**

## 目的

`retrieve` が `clinical_links` を関係メタデータとして返すだけでなく、リンク先 Bead とその
縦方向コンテキストを同じ token-budgeted bundle に入れる。これにより、検索語に直接一致しない
関連検査・処方・病態も、一回の MCP 呼び出しで生成 AI に渡せるようにする。

## 契約

- `include_links` の既定は true。true は sidecar と context expansion の両方を有効化する。
- `include_links=false` は両方を無効化する。通常の anchor/ancestor/descendant DAG は変えない。
- `link_depth`: clinical_links BFS 深さ。既定 1、最大 3。
- `max_linked_beads`: 文脈候補へ昇格するリンク先数。既定 20、最大 100。
- 展開元は anchor と、その `chain_depth` 内の ancestor/descendant 全候補。token packing 前に決めるため、
  小さい token budget でもリンク発見集合は変化しない。
- linked endpoint は anchor の次の優先 tier で L0。`provenance=clinical_link`、`via_link_id`、
  `link_depth` を付ける。リンク先の ancestor/descendant も通常規則で候補化する。
- `link_expansion.candidate_count` は上限適用前の到達数、`expanded_bead_ids` は上限適用後の候補、
  `truncated` は上限打ち切りを示す。token budget の打ち切りは従来どおり `truncated_refs` に出す。

## 安全性と決定論

1. 患者単位で clinical_links を一括取得する（item ごとの N+1 を禁止）。
2. 両 endpoint に `bead_status` を適用する。retracted は除外、amended は current へ置換、
   unattested は既定除外する。
3. 両 endpoint に clearance を適用し、一方でも不可視ならリンク自体を除外する。
4. 患者 Pod 外の endpoint は制御エラーとし、cross-patient 展開しない。
5. 優先順は BFS depth → severity（critical/alert/warning/info）→ evidence_basis
   （guideline/curated_knowledge/cooccurrence）→ created_at 新しい順 → link_id。
6. `projection_run_id` を sidecar に返し、展開根拠を projection manifest まで追跡可能にする。
7. `bead_status` が完全空の開発 store だけ absent=active。表が存在して一部 ID が欠ける場合は
   fail-closed の制御エラーとする。

## 同時に修正した完全性問題

- L0 の string-only rendering は JSON number/bool を落としていたため、検査値・単位・フラグを保持する
  deterministic JSON rendering へ変更。
- amended Bead の ID だけ current に変えて旧 Text を残す経路を廃止。current Bead から本文・型・時刻・
  token cost を再生成し、token budget を再適用する。

## 非目標

- clinical_links を正本 Bead や parents edge に変換しない。
- 患者をまたぐ展開を行わない。
- 無制限 traversal を許さない。
- cooccurrence を warning 以上へ昇格しない（link projector の既存不変条件を維持）。
