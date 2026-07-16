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

## 2026-07-11: U5 完成(API 語彙 + retrieve 既定挙動)

- **U5a(ba79c41)+ U5b(178ae7a)push 済み・reviewer GO×2**。U5 完成。
- **U5a**: 旧 sibling/APC 全除去(apc パッケージ + graph sibling tier + write.go 特例 + MCP ツール4つ)。
  clinical_links が唯一のリンク機構に。不変条件は既存+新テストで継続 pin。
- **U5b**: retrieve が bead_status を4経路で消費(retracted 除外 / amended→current 置換[patient scope 後・
  current=NULL は drop]/ unattested 除外[明示フラグで not_for_clinical_action、clearance は必ず通す])+
  BeadStatusFor バッチ(N+1 回避)+ API 改名(antigens→tags / include_siblings→include_links /
  search_antigens→search_tags、clean cut)+ 空 bead_status フォールバック(absent=active)。
  reviewer が mutation テストで must-fix 弁別性を実証。留保(amended-null drop の theater)はリードが
  resolveStatus 単体テストで境界を直接 pin して解消(空置換 mutation で FAIL を実測確認)。
- **failure catalog #13/#14 承認**: 複数フィルタ経路の取りこぼし / drop→ゼロ値置換の partial theater。
- **既知 follow-up(U6 前後で対応)**: bench/ の Python(mcp_client.py 等)が旧語彙 antigens/search_antigens を
  参照 → Go/MCP 側は完全改名済みだが Python consumer の追随が必要。

## 2026-07-11: U6 clinical_note 取り込み 設計 — Codex peer 統合(条件付き GO → 確定)

- **peer**: data-reviewer[Claude] + codex に同一中立問題文を並列投入(2つの LLM 検証)。両者とも**条件付き GO**。
  統合仕様 = specs/U6_clinical_note.md。
- **合意点(採用)**: base64→raw_text デコード・生 base64 は content 不投入 / clinical_note 専用 flattener
  (DefaultFlattener 内で分岐、raw_text 順序保存・summary は先頭)/ 本文 NLP tags は NO-GO(決定論違反)・
  untagged 既定 / nested context.encounter[0] を親に(fallback は patient root、silent 禁止で count)/
  sections[] SOAP 分割は後回し / U6a(Python+flattener+bench 小規模検証)/ U6b(実ストア再 ingest ~3.5h リード実行)分割。
- **data-reviewer が実データで発見した2事項(codex 未指摘、実証済み)**:
  ①**superseded 氾濫**: DocumentReference の 97%(953/983、私のサンプルでも 18/19)が status=superseded
   (Synthea が受診ごとに累積ノート再発行)。無条件全件だと ~37K near-duplicate Bead が FTS/retrieve/judge を破壊。
  ②type の LOINC coding を content に入れると antigen.Extract が文書種別を誤タグ化 → untagged 方針と矛盾。
   coding[] 構造を content に残さない(文字列フィールドに留める)。
- **実証で決着した crux(bench 軸)**: include_links は Items 不変(TestRetrieve_IncludeLinksFalse_
  LeavesContextBundleUnaffected が保証)→ dag_full/dag_nosib を include_links で区別すると同一数字の重複計測。
  → **2アームを単一 dag に統合**(sibling 概念は U5a で消滅)。apc_trigger 呼び出しは削除、reproject は CLI。
- **ユーザー裁定**: **superseded ノートは取り込まず status=="current" のみ ingest**(過去ナラティブ破棄。
  最新の累積ノートが実質全履歴を含む。過去時点の追跡[UC4]が要れば amends チェーン化を将来別ユニット)。

## 2026-07-13: R9 projection-link expansion 実装

- **決定**: `include_links` を sidecar-only から、患者内 clinical_links の bounded context expansion へ拡張。
  既定 depth=1 / max=20、上限 depth=3 / max=100。明示 `include_links=false` で sidecar と展開を共に停止。
- **安全条件**: 両 endpoint に status→clearance を適用、cross-patient 禁止、severity/evidence 優先、
  policy truncation と token truncation を別々に応答へ明示。
- **同時修正**: 数値/bool が L0 から消える string-only rendering、amended ID に旧本文が残る置換、
  populated bead_status の部分欠落を active 扱いする fail-open を修正。
- **仕様**: specs/R9_projection_link_expansion.md。

## 2026-07-14: R10 患者単位の自動増分投影

- **決定**: 患者Bead追記時に IndexBead + clinical_links + record_state + patient watermark を
  同一SQLite transactionでcommitする。通常追記は当該患者だけを処理し、全患者Reprojectはしない。
- **clinical_links**: 新規Bead接続分だけの追加方式は不採用。患者内頻度閾値とlink capにより、
  新規1件で過去同士の適格性も変わるため、正確な患者単位全置換を採用。
- **record_state**: 通常Beadと未承認amendmentは新規行のみ、過去を変えるattestation/retractionだけ
  患者チェーン全解決。
- **回復**: migration 0008 patient_projection_state を追加。Pod append後SQL commit前の停止は、
  次回OpenがCatchUp後にwatermark不一致患者だけ再投影してからserveする。
- **世代**: projection_manifest はknowledge/codeの解釈世代、
  patient_projection_state は患者ごとのデータ到達点。knowledge/code変更のローリング化はR11で実装。
- **実データ確認**: Synthea 1,135 Bundleのfilename-sort先頭10患者をscratch ingestし、
  10患者 / 4,202 patient Bead + 1 shared rule Bead / clinical_links 492件 / 失敗0を確認。
- **仕様**: specs/R10_incremental_patient_projection.md。

## 2026-07-14: R11 link_rule v2 / 患者優先ローリング更新

- **決定**: knowledge/code変更時のOpen同期全患者Reprojectを廃止。新manifestを目標世代として登録し、
  patient_projection_stateの世代不一致を仮想queueとして一患者ずつ移行する。100万件のqueue行を一括
  INSERTせず、patient_reprojection_queue実表は失敗・再試行だけを保持する。
- **優先度**: 既定3年以内の受診患者、長期未受診、deceased hintの順。hintはschedule専用で臨床状態に
  使用しない。legacyの死亡不明は高優先度側に倒す。
- **即時経路**: 新規追記・Pod recoveryはqueueを飛び越し、当該患者を現行世代へ同一transactionで更新。
- **link_rule v2**: min_shared/頻度閾値/link cap/score weightを実処理へ接続。v1読取互換を維持。
  curated ruleはauthor、表示revision、effective period、外部evidence Beadを保持可能。
- **複数根拠**: clinical_links自然キーへrule_versionを追加。同一Bead対・関係・tagを複数ruleが支持しても
  最後のruleで上書きせず、独立したassertionとして全根拠を保持する。
- **運用**: serveは小バッチのbackground drain、reprojectはbatch-size/3年閾値/drainを選択可能。
- **世代分離**: link code版とrecord_state contract版を分離。link-onlyデプロイで訂正状態の同期全再投影を
  起こさない。訂正チェーン意味論を変更した場合だけrecord-state-code-versionを更新する。
- **仕様**: specs/R11_prioritized_rule_rollout.md。

## 2026-07-14: R12 組織署名 / knowledge release

- **決定**: SHA-256(content同一性)と署名(組織・記載者の真正性)を分離。既存`attestation`は訂正承認に
  使用中のため、暗号署名はsubjectをparentに持つ不変`signature_attestation` Beadとする。
- **単一病院**: 電子カルテで認証した`actor_id`を、病院のシステムEd25519鍵で署名する。author(記載者)と
  signer(病院システム)を分け、病院名は表示、安定`organization_id`を信頼判断に使う。
- **trust policy**: tenant、複数組織、公開鍵、鍵用途、鍵有効期間/失効、必要承認人数をoperator管理する。
  秘密鍵はEngine/Pod/SQLiteに渡さない。ローカル鍵CLIはbootstrap専用、本番はKMS/HSMへ置換する。
- **purpose分離**: `clinical_origin`、取得変換だけを証明する`fhir_import`、ルール公開の
  `knowledge_release`を混同しない。
- **knowledge release**: releaseが宣言した閉じたlink_rule集合と署名数が一致した場合だけrolling targetへ
  切替。未承認rule混入、改ざん、未信頼/失効鍵、適用期間外はfail closed。serve起動時もactive manifestを
  再検証する。
- **互換**: hash対象外の旧`bead.Signature`は読取互換のため残すが、信頼判定には使用しない。
- **用語**: ルールの「施行日」を廃止し、`effective_from/to`は「適用開始/終了」と表記。
- **仕様**: specs/R12_signature_attestation_and_release.md。

## 2026-07-14: R13 FHIRサーバ連携の設計境界

- **現状**: Synthea file Bundleの決定的変換は実装済み。FHIR server接続、差分/version/delete、Provenance、
  checkpoint/quarantineは未実装。
- **決定案**: source原文の`fhir_resource_snapshot`と臨床利用Beadを二段階化する。現在のMedication参照
  inline解決やDocumentReference本文抽出をsource改変と混同しない。
- **同期**: 初回はBulk Data `$export`またはページ検索、差分はhistory `_since`優先、Subscriptionはtrigger、
  `_lastUpdated` fallbackはoverlap+content-address重複排除。page成功後だけcheckpointを進める。
- **整合性**: server/type/logical-id/versionのsource key、digest、patient/Encounter同一性、未解決参照、
  deleteを明示管理。Patient rootへのsilent fallbackやdropを禁止しquarantine/receiptへ記録する。
- **署名**: FHIR Provenance署名を検証できた場合だけ`clinical_origin`、connectorによる取得変換は
  `fhir_import`として署名する。
- **仕様**: specs/R13_fhir_server_sync.md。

## 2026-07-14: 論文改訂後の次期開発スケジュール

- **順序**: `manuscript_v3-codex` でMedBeadsのコンセプト論文を完成させた後、ローカルにFHIRサーバを
  構築し、Synthea由来の約1,100症例を用いてMedBeadsとの連携テストと実装開発へ進む。
- **対象**: R13で設計した初回取り込み、差分同期、FHIR version/delete、checkpoint、quarantine、
  source snapshotから臨床Beadへの変換、Provenance/署名連携、および障害後再開時の整合性を段階的に検証する。
- **論文との境界**: これは今後の開発スケジュールであり、今回のコンセプト論文の本文・Future Workには
  約1,100症例のローカルFHIRサーバ連携計画として記載しない。

## 2026-07-15: R14 患者同一性・cross-patient graph contamination 防止（将来実装）

- **位置付け**: 患者間リンク汚染への対策方針は設計として確定したが、MedBeads本体には未実装。
  R13のFHIR server同期を実装する際、patient mapping/quarantineのproduction invariantとして組み込む。
- **二重の境界**: 構造的混入は`patient_root`制約で拒否する。source EHRが別人へ一貫して誤ったPatientを
  割り当てる意味的誤同定は構造だけでは検出できず、namespaced identifier、MPI assurance、組織署名、
  監査・人手adjudicationを必要とする。
- **ingest**: patient-scoped Beadに`expected_patient_root`を要求し、通常parents/amends/retractsのroot不一致、
  subject不一致、未解決参照はappendせずquarantineする。複数患者rootから`_shared.pod`へ黙って
  フォールバックしない。
- **DB/監査**: `clinical_links`は`root(a)=root(b)=patient_root`をINSERT/UPDATE triggerでも強制する。
  `verify_integrity`をPod metadata、Bead edges、訂正関係、clinical_linksのroot監査へ拡張する。
- **多施設患者同一性**: FHIR `Patient.link`/`Person.link`を入力根拠とし、承認・組織署名・assurance・
  consent/purpose/clearance・有効期間・撤回履歴を持つ専用`patient_identity_link`へ昇格する。通常の
  `parents`/`clinical_links`とは分離し、root/Podは統合しない。
- **互換性**: 現在のshared-parent利用を先に監査し、共有知識参照をEvidenceまたは専用knowledge referenceへ
  移行してから通常parentの同一root制約を有効化する。
- **仕様**: specs/R14_patient_identity_and_partition_integrity.md。

## 2026-07-15: R15 法域中立の医療フェデレーション（将来実装）

- **coreと法域の分離**: MedBeads coreは特定法を埋め込まず、不変Bead、署名、患者同一性、訂正、
  clinical links、purpose、clearance、release manifest、audit receiptを共通機構とする。米国Cures/ONC、
  EU EHDS/AI Act、日本のsecondary use等はversioned regulatory profileとして外付けする。
- **三契約の分離**: token-budgetedなAI向け`retrieve`、要求範囲を省略しないFHIR/EHI完全export、
  仮名化・最小化・secure environmentを伴うsecondary-use releaseを相互に代用しない。
- **federation**: 将来のP2Pはpublic networkではなく、認証・契約された役割付きnodeによるpermissioned
  federationとする。control planeのtrust/identity/permitと、暗号化されたdata planeを分離する。
- **識別子保護**: Bead IDをpublic network locatorとして広告しない。研究releaseは仮名化後の新しい
  canonical object/IDを生成し、元patient root・FHIR identifier・source Bead IDとの対応は分離vaultに置く。
- **derived interpretation**: 受信施設は署名・manifestを検証し、自施設のknowledge/policy世代で
  clinical linksを再構築する。外部derived linkを無条件に正本化しない。
- **blockchain/IPFS**: coreの必須要件にしない。private P2P transportは後から選択可能。blockchainは
  中央運営者なしの共有監査logが必要と実証された場合にcommitment用途へ限定し、医療情報本体を載せない。
- **論文境界**: 現在の国際コンセプト論文へ日本固有法または各国法の詳細な適合主張は追加せず、
  将来実装・別論文・法域別deployment profileとして扱う。
- **仕様**: specs/R15_jurisdiction_neutral_federation.md。

## 2026-07-15: R16 公開論文デモと本番開発の分離

- **公開GitHub版**: 論文読者がDockerで短時間に再現するreference implementationとする。Synthea等の
  合成10症例、`viewer`、localhost限定、外部API key不要、新規患者登録なしを既定とする。
- **本番開発**: 共通coreを利用するが、FHIR endpoint、患者identity mapping、実データ、trust policy、
  service credential、秘密鍵、監視・backup等はprivate deployment overlayへ分離する。
- **分離原則**: デモ用core forkや安全検証の迂回は作らない。分離するのはデータ、権限、接続、運用設定、
  配布物と主張であり、Bead hash、Pod、clinical links、retrieveの意味論は共通に保つ。
- **Docker**: 公開repoの既定composeはpaper demoだけを起動する。本番composeを公開デモの単純な環境変数
  差し替えとして提供せず、施設側がrelease tag/image digestをpinしたprivate overlayを管理する。
- **表示**: R13/R14、KMS/HSM、監査、障害・負荷・規制検証を満たすまではproduction readyと表記しない。
- **仕様**: specs/R16_public_demo_and_production_boundary.md。

## 2026-07-15: R16 公開paper-demo Docker実装

- **配布物**: root `Dockerfile` + `compose.yaml`（Go core / Nginx+React UI）を追加。既定は
  `viewer`、service tokenなし、localhost bind、非root core、合成データlabelとした。
- **再構築**: 合成10患者のPod正本3.6MBだけをcommitし、SQLiteは配布しない。image build内で
  `verify`→`reindex`→`reproject`を実行し、Podからschema v11とderived linksを再構築する。
- **実測smoke test**: Docker Desktop arm64で10患者 / 4,202患者Bead / 4,192 parent edge /
  492 clinical links、UI health、Nginx API proxy、全11 Pod・4,204 frame検証OKを確認。
  `POST /patients`と`POST /beads`は405。clearance派生変更はcontainer再作成で初期化された。
- **UI依存**: `npm audit fix`（`--force`なし）で40 packageを更新し、65 GitHub alertの原因だった
  npm既知脆弱性を本番依存・開発依存とも0件にした。36 UI testとproduction buildは成功。
- **回帰防止**: CIにamd64 Docker build、10患者、492 links、proxy、登録拒否、Pod verifyを検査する
  `Paper demo (Docker)` jobを追加。
