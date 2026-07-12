# R7: Bead Graph View — 二軸(縦=時系列DAG / 横=clinical_links)

## 目的

任意の患者について、Bead の二方向の繋がりを1画面で可視化する**実機能の UI ページ**。
- **縦軸 = 時間**: 親子 DAG(registration → encounter → observation/medication/note)+ 訂正チェーン(amends/retracts)
- **横軸 = clinical_links**: co-occurrence / curated_knowledge / guideline(relation + severity + evidence_basis)

このページを実データ1患者で表示 → 画面キャプチャ → 論文の v2 図を置き換える。全 1,135 症例で描画可能。

## 契約(リード固定・2026-07-12 ユーザー承認)

**1エンドポイントに集約。** clearance マスク・retracted/amended 正規化は MCP `get_links` と同じ経路を通す。

```
サーバ登録パス:   GET /patients/{root}/graph        （Go http.ServeMux、root マウント、Go1.22+ path param）
フロントからの呼出: GET /api/core/patients/{root}/graph （api.ts baseURL=/api/core を Nginx/Vite proxy が root へ張替）
```
（訂正 2026-07-12: 当初 `/api/core/...` をサーバパスと誤記。実リポジトリは REST を root にマウントし全パス
フラット。`/api/core` はフロント baseURL のみ。go-builder 実装 `/patients/{root}/graph` が正。）

レスポンス(200):
```json
{
  "patient_root": "<bead id>",
  "beads": [
    {
      "id": "<bead id>",
      "type": "fhir_observation | ... | clinical_note",
      "timestamp": "<clinical event time, RFC3339>",
      "recorded_at": "<write-instant, RFC3339, may be empty>",
      "summary": "<machine one-line summary>",
      "status": "active | amended | retracted | unattested",
      "current_bead_id": "<id or empty>",   // amended の置換先(status=amended のとき)
      "amends": ["<id>", ...],              // この bead が訂正する対象（0..n、bead.Amends[] をそのまま）
      "retracts": ["<id>", ...]             // この bead が撤回する対象（0..n、bead.Retracts[] をそのまま）
    }
  ],
  "edges": [                                // 縦: parent DAG
    { "child_id": "<id>", "parent_id": "<id>" }
  ],
  "links": [                               // 横: clinical_links(患者スコープ)
    {
      "link_id": "<id>",
      "bead_a": "<id>", "bead_b": "<id>",  // bead_a < bead_b(undirected)
      "relation": "<typed relation>",
      "matched_tag": "<tag>",
      "severity": "info | warning | alert | critical",
      "evidence_basis": "cooccurrence | curated_knowledge | guideline",
      "rule_version": "<rule bead id or empty>"
    }
  ]
}
```

### 契約の要点(実装が守ること)

- **beads**: `ListPatientBeads` 相当(patient_root スコープ、timestamp 昇順)。ただし `recorded_at` と
  `status`/`current_bead_id`/`amends`/`retracts` を各 bead に付与する(現 `BeadRef` は未搭載 → 拡張)。
  status は `bead_status` 表由来。空 bead_status は absent=active フォールバック(U5b と同じ規約)。
- **edges**: `bead_edges` の `edge_type='parent'` のみ。sibling は死文化なので出さない。
  amends/retracts は edge ではなく beads[].amends/retracts フィールドで表現(縦の訂正チェーンは
  フロントがそこから描画)。
- **links**: `clinical_links` の patient_root スコープ全件。新規 `DB.GetClinicalLinksForPatient(root)`
  で取得(既存 `idx_clinical_links_patient_sev` を使う。per-bead の `GetClinicalLinks` を N 回呼ばない)。
  MCP get_links と同じく retracted/unattested を端点に持つリンクは status 正規化で除外/置換。
- **clearance**: 患者スコープの beads/links は clearance マスクを通す(既存 MCP 経路を再利用)。
  マスクされた bead はレスポンスから除外し、その bead を端点に持つ edge/link も落とす(dangling 防止)。
- **beadView は広げない**: REST の `beadView`(views.go)は v2 凍結契約。この graph 用に**新しい view 型**
  (`graphBeadView` 等)を `internal/rest/views.go` に足す。

## 実装ユニット

- **R7a (go-builder)**: `DB.GetClinicalLinksForPatient(root)` 追加 + `ListPatientBeads` を recorded_at 込みに
  拡張(または新関数)+ bead_status バッチ結合 + REST `/patients/{root}/graph` ハンドラ + 新 view 型。
  clearance/status 正規化は MCP get_links / retrieve の既存ヘルパを再利用。
- **R7b (ts-client-builder)**: `GraphView.tsx` 拡張 — 横軸に clinical_links エッジ(severity で色/太さ:
  info=淡グレー細線、warning/alert/critical=琥珀→赤・太線)、訂正チェーンを amends/retracts から破線、
  状態を色符号化(緑=active / 琥珀=amended / 取消線=retracted / 破線=unattested)。api.ts に graph 取得追加。
  患者選択で全症例描画可能に。
- **R7c (data-reviewer)**: 契約一致(全フィールド)・clearance 継承・dangling edge/link なし・
  per-patient links クエリがインデックスを使うか(EXPLAIN)・スケール(最大 Bead 患者で崩れないか)検証。

## 完了条件

各ユニット `CGO_ENABLED=1 go test -tags sqlite_fts5 ./... -race` green + reviewer GO + checkpoint commit
(メッセージに R7# を含める)。最後に実データ1患者で画面キャプチャ → 論文図差し替え。
