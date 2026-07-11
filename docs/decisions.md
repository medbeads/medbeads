# 意思決定ログ(decisions)

プロジェクトの方向を変えた決定を時系列で記録する。各項目 = 決定 / 背景 / 根拠の所在。

## 2026-07-09: v3 統合再構築の承認

- **決定**: v1 漸進改善案を破棄し、v3 としてストレージ・検索・プロセス構成を再設計する
- **記録**: docs/requirements.md v2、specs/DESIGN_v3.md

## 2026-07-10: M1 コア再構築の完了認定

- **決定**: 性能3目標(バンドル <10ms / FTS <50ms / retrieve p95 <500ms)を実測で達成、
  M1 完了基準を満たしたと認定
- **証拠**: bench/perf_results/M1_EVIDENCE.md(1,135患者・104万 Bead)

## 2026-07-10: 方向転換 — MICCAI 実験ドリブンの停止(ユーザー指示)

- **決定**: MICCAI 2027 に向けた M2 実験パイプラインの実行を停止。重い LLM 実験は将来
  H200 サーバーで実施。当面の北極星を**「電子カルテ(人が書くメモ)に対抗できる、
  生成AI のためのカルテ」としての品質**に再定義
- **帰結**: M2 評価ハーネスは検証済み資産として凍結保全(commit 78b6ed3)。
  埋め込みバックフィルは凍結(queue 残 ~29万件、スキーマ改定後に再実行)
- **注**: docs/requirements.md §1 の「研究ゴール = MICCAI 2027」はこの決定により凍結
  (削除ではなく延期 — 実験装置は完成している)

## 2026-07-10: スキーム批判レビュー(3視点並列)の実施

- **方法**: 同一の中立的問題文を、互いの回答を見せずに3レビュアーへ並列投入
  (A: 内部整合性 / B: Codex 外部設計 / C: 臨床ウォークスルー)
- **全文**: docs/reviews/2026-07-10_scheme_critique_{A_internal,B_codex,C_clinical}.md
- **収束した本丸4点**: ①自由記述・アセスメントの器が無い ②「今の状態」が導出不能
  ③リンクの87%が無意味な共起・要約が未実装同然 ④訂正・取り消しの機構が皆無

## 2026-07-10/11: v3.1 スキーマ改定の決定(ユーザー裁定)

- **決定**(specs/DESIGN_v3.1_draft.md に全文):
  1. antigens をハッシュ対象から除外し Bead から撤去(事実と解釈の二層分離)
  2. sibling_link を Bead から index 投影へ降格(検索速度問題は実測で解消済みのため。
     「日次成長への増分追随」を GraphRAG 差別化の柱に格上げ)
  3. 免疫メタファーは説明装置として維持、API 語彙は標準語へ
  4. clinical_note / assessment / attestation / retraction 型 + amends/retracts を
     ハッシュ規約に焼き込み(器のみ v3.1。AI 起草→医師承認のワークフローは将来機能)
  5. **未承認 Bead は retrieve 既定で除外**(LLM はラベルを軽視し未承認推論を既成事実化
     しやすいため — 臨床安全優先)
  6. **訂正・承認の関係は参照先のクリアランスを継承**(制限中の記録の存在自体が PHI に
     なりうるため関係も隠す。監査ロールには全可視)
- **peer レビュー**: data-reviewer + Codex の2件(いずれも条件付きGO → 条件を v3.1.1 で解決)
- **実装分割**: U1(ハッシュ規約)→ U2(投影スキーマ、Codex peer 必須)→
  U3(link projector)→ U4(状態導出)→ U5(API 語彙)→ U6(再 ingest + bench 軸再定義)

## 2026-07-11: 論文改訂と公開整備の決定(ユーザー指示)

- **決定**: arXiv 2602.01086 を本格改訂(新フォルダ 0710_manuscript_v3/、本文書き直し可)。
  投稿は U2〜U6 完了後(「実装済み・実測済み」として主張を強くする)。
  GitHub は Apache-2.0 ライセンスを付与して公開整備

## 2026-07-11: Track B(GitHub 公開整備)完了

- **実施**: Apache-2.0 `LICENSE`(正準全文)+ `NOTICE`(著作権 Takahito Nakajima + PHI/Synthea 声明)
  + `CITATION.cff`(arXiv:2602.01086、cffconvert で schema 1.2.0 準拠を検証)+ README 3言語の
  引用ブロックを arXiv 引用に差し替え・License 節追加。commit `a9877de` → push 済み。
  GitHub は Apache-2.0 を認識(`gh api repos/.../license` → Apache-2.0)。
- **公開衛生**: 秘密情報スキャン clean(ハードコード鍵なし、env 読み込みのみ)、
  FHIR_sample は Synthea 合成データ(provenance マーカー確認、実 PHI なし)。data-reviewer GO。
- **Dependabot triage メモ**: 65件(high 31 / mod 25 / low 9)は**すべて `ui/package-lock.json`
  (React v2 ビジュアライザ)由来**。Go エンジン(本番面、go.sum)は対象外。UI は v3 で
  deprecated 予定のため、本番リスクは低。対応は P2 以降(UI を扱う際にまとめて更新 or UI 廃止)。

## 2026-07-11: U2 投影スキーマ設計 — Codex peer 統合(条件付き GO → 確定)

- **peer**: data-reviewer(Task, opus)と codex exec(read-only)へ同一中立問題文を並列投入。
  両者とも**条件付き GO**。統合仕様は specs/U2_projection_schema.md に確定。
- **合意点(採用)**: 実データ表は現行世代のみ・置換方式(世代共存却下)/ 各行に projection_run_id /
  bead_antigens→bead_tags は VIEW 却下・新設+U3 一斉切替+フル reindex・二重書き禁止 /
  projection_manifest は追記専用 / bead_tags は 0005 の3索引を漏れなく引き継ぐ / matched_tag を
  独立列に / 投影表に beads(id) FK を張らない。
- **実証で決着した crux 2件**:
  1. `beads.recorded_at` 追加は必須(実証: beads 表に該当列なし、Pod meta の WrittenAt が実体だが
     未投影。訂正チェーン解決 `recorded_at → bead ID 辞書順`に不可欠)。0006 で ALTER 追加、
     充填は U3 のフル reindex 時。
  2. sibling_link Bead 廃止で clinical_links は「Pod のみ再構築」→「Pod + 知識 Bead + projector
     コード版」へ。round-trip テストの不変条件を U3/U4 で**強化**(緩和ではない)。U2 では
     clinical_links を埋めないので既存テストは無傷。
- **スコープ確定**: U2 = スキーマの器 + recorded_at 追加まで(新表は空・旧経路無傷 → 中間半端状態が
  構造的に発生しない)。書込切替・link projector は U3。active_conditions/medications の物理表化は
  U4 で計測後判断(過剰設計回避)。

## 2026-07-11: U3 link projector 設計 — Codex peer 統合(条件付き GO → 確定)

- **peer**: data-reviewer + codex に同一中立問題文を並列投入。両者とも**条件付き GO**。統合仕様 =
  specs/U3_link_projector.md。
- **合意点(採用)**: 3分割(U3a タグ経路 atomic swap + recorded_at / U3b link projector 新設 / U3c read 切替 +
  テスト強化)/ U3a は書込+読取6箇所+full reindex を1原子ユニット(片側だけ切ると空テーブル読取の
  half-migrated)/ U3b は scanner 改造でなく新設(bead_apc_scan/generation/sibling_link 依存を延命しない)/
  link_rule Bead は content-addressed JSON・U3 では共起 info のみ・warning は後続 / rule_version=link_rule Bead ID /
  不変条件を「Pod のみ」→「Pod + knowledge Bead IDs + config_hash + code_version」へ明示強化 / round-trip テストは
  削除でなく置換 / created_at・link_id を内容導出で決定論化。
- **実証で決着した crux**: 全再投影の方式。単一トランザクション全 DELETE→INSERT は 104万規模で NO-GO
  (実証: SetMaxOpenConns(1)・busy_timeout=5000・WAL、現状 reindex は既に batchSize=500 で per-tx 分割 →
  単一 tx は既存設計に逆行、writer ロック長期保持で日次 Ingest が5秒タイムアウト)。→ **patient_root バッチ +
  manifest active フリップ**を採用。Reproject(知識更新の投影入替、Pod 再スキャンなし)を Reindex(index.db
  消失復旧)と別関数に分離。codex の実測ゲート(1,135患者で <15分)を U3b done 条件に。
- **U3 前の仕様修正**: DESIGN_v3.1 の「Pod のみ再構築」文言を強化版に修正。link_rule Bead content スキーマ確定。
- **failure catalog 起案2件**(要承認): ①知識更新の全再投影を Reindex で実装しない(Reproject と分離)
  ②大量投影入替を単一 tx にしない(原子性の単位を patient_root に落とす)。

## 2026-07-11: U3 完成 + failure catalog 5件承認

- **U3(link projector)完成・全 push 済み**: U3a(1acfe40 タグ経路)/ U3b(073be85 projector 新設)/
  U3c(56d6fc6 read + clearance 継承 + 不変条件強化)。すべて reviewer GO。
- **failure catalog 8〜12番をユーザー承認** → team-lessons 追記(計12件): FTS5 構文言語 / resume 集計は
  成果物から再導出 / gofmt 日本語コメント / 知識更新は Reproject(Reindex と分離)/ 大量投影入替は
  patient_root 単位 tx。

## 2026-07-11: U4 状態導出 設計 — Codex peer 統合(条件付き GO → 確定)

- **peer**: data-reviewer + codex に同一中立問題文を並列投入。両者とも**条件付き GO**。統合仕様 =
  specs/U4_state_derivation.md。
- **合意点(採用)**: チェーン解決は Go 実装(SQL は順序スパイン + patient tx のみ)/ status projector は
  Pod デコード方式(amends/retracts edge を index に先行投影しない)/ bead_status に patient_root を 0007 で追加 /
  active_conditions/medications は物理投影表(SQL VIEW 不可 = content が Pod のみ)/ 別 projection_name
  "record_state_v31" で bead_status + active_* を同一 run / §2 は固定順序(retracted→attestation→amends)/
  U3 follow-up(loadRule の knowledgeBeadIDs 配線)を同梱。
- **data-reviewer が発見した correctness 穴2件(実証済み・GO ブロッカー)**:
  ①retraction Bead が parents 空だと共有 Pod に落ち患者スコープから外れる(ingest.go:257 で実証)→
   対策: retraction/attestation は対象を parents に含めることを要求 + クロス Pod fixture。
  ②ListPatientBeads は timestamp(臨床時刻)順で、訂正解決に要る recorded_at(記録時刻)順と軸が違う
   (read.go:62 で実証)→ 対策: デコード後 §2 キー `(recorded_at IS NULL) ASC, recorded_at DESC, id DESC`
   で再ソート + NULL/遅延 amendment fixture。
- **ユーザー裁定**: **retraction(取り消し)は attestation 承認不要・即時有効**(entered-in-error は誤データを
  承認待ちの間臨床的に生かさない = 臨床安全優先。content.authorized_by が権限担保。「retracted 最強」と整合)。
  amends(訂正)は承認必須のまま。

## 2026-07-11: U4b 完成 + notes 事実修正

- **U4 完成・全 push 済み(reviewer GO×2)**: U4a(e5b1cd1)/ U4b(b8ddc0a record_state projector)。
  builder agent が stream-watchdog timeout で死んだが成果物は健全(全テスト green)→ reviewer が白紙から
  mutation テストで must-fix の弁別性を実証。実測: bead_status 全 active、active_conditions 47・medications 17。
- **論文 notes(0710_manuscript_v3/notes/)の事実修正**: 実装照合(Explore)で判明した誤りを是正 —
  Bead 総数を 960,443(v3.1 基底、sibling 込み 1,042,456 と区別)/ reindex 5m40s(基底)vs 7m31s(sibling 込み)/
  clinical_note は型名のみ・取り込みは U6 未実装 / attestation-assessment の処理は U4(器は U1 でない)/
  retrieve provenance は U5 未実装。demo_data を現行実装で reproject し直して 706/47/17 を再実測確認。

## 2026-07-11: U5 API 語彙 + retrieve 既定挙動 設計 — Codex peer 統合(条件付き GO → 確定)

- **peer**: data-reviewer[Claude] + codex に同一中立問題文を並列投入(ユーザー指示「2つの LLM で検証」)。
  両者とも**条件付き GO**。統合仕様 = specs/U5_api_retrieve.md。
- **合意点(採用)**: 2ユニット分割 / status 正規化を全 read 経路(anchor/items/truncated_refs/anchor_ids の4経路)/
  順序 status→GetBead→clearance / BeadStatusFor(ids) バッチ(N+1 回避)/ retrieve は active manifest で絞らない
  (patient 単位 DELETE で最新 run のみ物理存在)/ current=NULL の amended は drop・置換は patient scope 後 /
  graph sibling tier 除去で文脈は痩せる(clinical_links は sidecar 代替、近傍展開は非代替)を仕様として受容 /
  MCP は clean cut(alias 不要、TS 契約影響ゼロ)/ 0008 DROP せず inert 化 / get_links も status を見る。
- **実証で決着した crux(分割順序)**: **②除去先行 → ①API/status**。retrieve.go:223/229 が sibling tier に
  依存し BuildContext が非 anchor Bead を item に展開(実証)→ ①先行だと除去対象コードパスに status 配線を
  通す二重工事。②先行なら痩せた bundle に status を足す単純問題に。
- **ユーザー裁定(空 bead_status フォールバック)**: 状況で分ける統合案を採用 — テーブル完全空(未 reproject・
  開発初期)は absent=active でフォールバック(retrieve が壊れない)/ 行はあるのに特定 ID 欠落(reproject 済み
  不整合)は制御エラー or 除外(異常を黙って通さない)。臨床安全 vs 開発実用性を両立。
- **分割**: U5a(旧 sibling/APC 除去、先行)→ U5b(API 改名 + retrieve 既定挙動)。
- **include_links の意味**: sidecar に留める(リンク情報を返すが context item に展開しない)。近傍展開は将来別設計。
