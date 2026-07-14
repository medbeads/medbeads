# MedBeads 要件定義書 v3

作成日: 2026-07-11 / 前版: v2(2026-07-09、git 履歴に保存)
v2 からの変更: 目的を「MICCAI 2027 実験」から**「MedBeads 自体の完成」**に再定義
(決定記録: docs/decisions.md 2026-07-10)。設計は specs/DESIGN_v3.md + specs/DESIGN_v3.1_draft.md。

## 1. 目的

**MedBeads を「電子カルテ(人が書くメモ)に対抗できる、生成AI のためのカルテ」として
完成させる。** 学会実験(MICCAI 等)は凍結中の将来項目(装置は完成済み、実行は H200 サーバー)。

中核の設計思想(v3.1 で確立):
- **事実と解釈の二層分離**: 不変の正本(臨床事実・人と AI の記述・訂正・承認・知識の実体)と、
  常に再構築可能な投影(タグ・リンク・状態・要約・検索構造)
- **日次成長への増分追随**: 記録の追加 = 患者内増分処理(ミリ秒〜秒)、知識の更新 =
  投影の再構築(分)。全体演算(GraphRAG 型の再クラスタリング)を持ち込まない
- 不変条件の維持: 内容ハッシュ = ID、Merkle DAG、Tamper-Evident、append-only、
  投影は正本から完全再構築可能

## 2. 利用者と完成の定義(ユースケース = 受け入れ基準)

主利用者: 医療 AI エージェント(MCP 経由)、研究者・医師(UI / 承認者)。

**完成の定義**: 以下の4場面で、AI が人間の SOAP メモと同等以上の文脈を、監査可能な形で
取得・生成できること(2026-07-10 臨床適合性レビューの4ウォークスルーを昇格):

- **UC1 外来再診の直前参照**: 「アクティブな問題・現行処方・前回からの変化」を1回の呼び出し
  (brief)で取得できる
- **UC2 退院サマリ作成**: 入院経過を根拠 Bead 付きで要約できる(assessment が根拠を
  parents で固定)
- **UC3 当直申し送り**: 重症度・鮮度で優先順位づけられた懸案リストを取得できる
- **UC4 インシデント振り返り**: 訂正履歴(amends/retracts)と二重時間軸(イベント時刻/
  記録時刻)で「その時点で何が見えていたか」を辿れる

## 3. 機能要件

### 3.1 実装済み基盤(v3 M1、証拠: bench/perf_results/M1_EVIDENCE.md)

- Pod ストレージ(患者パーティション・append-only・zstd)+ verify / reindex
- RFC 8785 (JCS) ハッシュ規約、SQLite 投影(FTS5 / vec0 semantic)、graph バンドル、
  clearance(mask-then-drop)、MCP 第一級(retrieve = トークン予算 + 切り捨て明示 +
  provenance)、REST(UI 契約凍結)
- 実測: 1,135患者・104万 Bead で バンドル 8ms / FTS 20ms / retrieve p95 172ms /
  verify 6.8s / reindex 7.5m

### 3.2 v3.1 スキーマ改定(実装中 — 詳細: specs/DESIGN_v3.1_draft.md)

| # | 内容 | 実装ユニット |
|---|---|---|
| N1 | ハッシュ規約: antigens 撤去 + amends/retracts 追加、タグ抽出を投影時へ | U1(進行中) |
| N2 | 投影スキーマ: bead_tags / clinical_links / bead_status / active_views / projection_manifest(世代台帳)、辞書・ルールの Bead 化 | U2(Codex peer 必須) |
| N3 | link projector: LOINC 共起除外、relation/severity のルール導出、evidence_basis | U3 |
| N4 | 状態導出: 訂正チェーン解決(決定論)、アクティブ問題・現行処方、未承認の既定除外、クリアランス継承 | U4 |
| N5 | API 語彙標準化: tags / get_links、retrieve 既定挙動(retracted 除外・amended 置換) | U5 |
| N6 | 再 ingest + clinical_note 取り込み(Synthea DocumentReference)+ bench 軸再定義 | U6 |
| N7 | 追記時の患者単位自動投影: index + clinical_links + record_state + watermarkを同一commit、起動時患者限定回復 | R10 |
| N8 | link_rule v2 + 知識更新の患者優先ローリング投影: 新規データ即時、最近受診→長期未受診→死亡hint | R11 |
| N9 | 組織/記載者/署名者分離、Ed25519 signature_attestation、署名済みknowledge release | R12 |
| N10 | FHIRサーバ差分同期、source snapshot、version/delete/Provenance、quarantine | R13(設計済み・実装予定) |

### 3.3 臨床品質(v3.1 実装後の次フェーズ)

- **Q1（実装済み）** FHIR 対応 flattener(summary =「メロペネム 1g 点滴静注 8時間毎」型)+
  L0 deterministic JSON rendering（数値検査値・bool を欠落させない）
- **Q2** `brief(patient_id, token_budget)` ツール(UC1 の入口 — anchor 不要の定型バンドル)
- **Q3** 詰め込み優先度(新しい順 + 異常フラグ/重症度。現状の「古い順に詰める」を廃止)
- **Q4** タグ辞書の拡充(RxNorm→ATC 公開クロスウォーク、LOINC→organ/risk —
  「eGFR低下↔腎排泄薬」の看板ユースケースを成立させる)
- **Q5** clearance の withheld_count(秘匿の存在をエージェントに通知)
- **Q6（実装済み）** projection-link expansion。状態・clearance 適用済み clinical_links を
  患者内 bounded BFS で retrieve Items に展開（specs/R9_projection_link_expansion.md）
- **Q7（実装済み）** 患者単位の自動増分投影。通常追記で全患者再構築を行わず、知識/コード世代変更は
  患者優先ローリングqueue（specs/R10_incremental_patient_projection.md、R11_prioritized_rule_rollout.md）

### 3.4 将来(凍結・未着手を明示)

- AI 起草 → 医師承認ワークフローの実装(器 = 型定義は N1 で完了)
- LLM 比較実験(4アーム、H200 サーバー。ハーネスは commit 78b6ed3 で凍結保全)
- KMS/HSM・DID鍵解決 / 多施設patient identity・同意 / FHIR server connector / EMR-CSV 取込 / migrate CLI

## 4. 非機能要件

- **PHI**: 実患者情報をコード・テスト・ログ・コミットに一切含めない(Synthea のみ)
- **性能**: M1 実測値を回帰基準線とする(バンドル <10ms / FTS <50ms / retrieve p95 <500ms)。
  **増分性**: 1患者の日次追加処理 <1s、知識更新は新規データ患者を即時更新し、残りをrate-limit可能な
  患者単位queueで処理（10万〜100万患者の実測SLOは今後のscale benchで確定）
- **完全性**: verify で全データの暗号学的検証、reindex で投影の完全再構築、
  訂正チェーン解決の決定論(同一正本 + 同一知識世代 → 同一状態)
- **真正性**: content hashだけを作成者証明とみなさない。署名必須運用ではoperator管理のtrust policy、
  鍵用途/失効、knowledge releaseを検証し、未承認ruleをactiveにしない
- **テスト**: 全ユニットで `-race` + reviewer 検証を経てから commit(従来ループ維持)
- **Python は uv 必須**(pip / poetry / conda 禁止)

## 5. ロードマップ

| フェーズ | 内容 | 状態 |
|---|---|---|
| P1 | v3.1 スキーマ改定(U1〜U6) | U1 進行中 |
| P2 | 臨床品質(Q1〜Q5)→ UC1〜UC4 の受け入れ確認 | 未着手 |
| P3 | 論文 v3 改訂(0710_manuscript_v3、投稿は P1 完了後)+ GitHub 公開整備(Apache-2.0) | 並行中 |
| P4 | 検証実験(H200)・実運用堅牢化 | 凍結(将来) |

## 6. 役割分担(go-ts-data チーム)

| 役割 | 担当 |
|---|---|
| go-builder | engine / 投影 / MCP / REST / bench Python(兼務) |
| ts-client-builder | UI の REST 契約追随(リンク・状態表示は P2 以降) |
| data-reviewer | 全変更の検証、スキーマ・ハッシュ規約・移行の正当性 |
| Codex peer | スキーマ・破壊的変更の並列独立レビュー(U2 で必須) |
| ユーザー(医師) | 臨床適合性の裁定、UC 受け入れ判定、論文最終確認 |
