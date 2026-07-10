**致命的**

1. **amends/retracts が「事実フィールド」だけでは訂正イベントとして弱い**

   `amends` / `retracts` を全 Bead のハッシュ対象フィールドに入れる案は、ID 安定性の観点では成立します。ただし event sourcing / FHIR Provenance / openEHR versioning の定石から見ると、訂正は「対象への属性」ではなく「新しい commit / contribution / provenance event」です。

   問題は、誰が、なぜ、どの権限で、どの時点で、どの旧版を、どの状態に遷移させたかが `amends: [id]` だけでは不足する点です。`content` に理由を書く運用では監査クエリが不安定になります。

   改善方向: `amends/retracts` フィールドは残してよいが、訂正行為は型で明示するべきです。少なくとも `correction` / `retraction` / `attestation` 相当の event Bead を第一級にし、`target_ids`, `reason_code`, `reason_text`, `effective_policy`, `agent`, `recorded_at`, `authorized_by` を構造化する。FHIR なら `Provenance.activity` / `AuditEvent` / `Observation.status=entered-in-error` への写像を明記する。

2. **retraction と amendment の状態遷移モデルが未定義**

   draft は `bead_status = active / amended / retracted / unattested` とするが、競合訂正、二重訂正、撤回の撤回、訂正 Bead 自体の訂正、複数 target の部分成功を定義していません。append-only ではここを曖昧にすると active problem list や retrieve の既定除外が非決定的になります。

   改善方向: 状態導出規則を仕様に落とす。例: `retracted` は最強、`amends` は target ごとに latest accepted amendment を選ぶ、選択キーは `recorded_at, bead_id`、未承認 amendment は current にしない、cycle 禁止、cross-patient amendment 禁止、target type 制約を置く。

3. **リンク投影の監査は manifest + 決定論だけでは「時点 T の主張」には足りない**

   「同じ正本 + 同じ code/dict/config なら再現可能」は研究再現性には十分です。しかし臨床監査の問いは「2026-07-10 10:15 に AI に提示されたリンク集合は何か」です。projection manifest だけだと、その時点でどの manifest が active だったか、部分再構築中だったか、retrieve がどの snapshot を読んだかが残りません。

   改善方向: リンク台帳の full snapshot は不要でも、`projection_manifest` を immutable にし、`projection_run_id`、入力 Pod watermark、対象 patient_root、code commit、dictionary hash、config hash、built_at、activated_at、superseded_at を持たせる。retrieve provenance には `projection_run_id` と `manifest_hash` を必ず返す。リンク行にも `projection_run_id` を持たせる。

4. **unattested Bead を既定 retrieve に含めるのは臨床安全上危険**

   draft は「隠さない、監査可能性優先」とするが、AI 起草や未承認 assessment が通常 retrieve に混ざると、エージェントが未承認推論を既成事実として再利用します。ラベル付きでも LLM コンテキストでは弱い安全策です。

   改善方向: 既定は `include_unattested=false` が妥当。明示指定時だけ返し、返す場合は `status`, `attestation_chain`, `draft_author`, `not_for_clinical_action` を構造化して provenance に載せる。監査可能性は get_bead / audit query で担保する。

**重大**

5. **recorded_at がハッシュ外 meta なのは監査時刻として弱い**

   `timestamp` と `recorded_at` の二重時間軸は正しい。ただし `recorded_at` が Pod frame meta だけでハッシュ外だと、「いつ記録されたか」という medico-legal に重要な事実が Bead ID で保護されません。

   改善方向: `recorded_at` を core hash に入れるか、少なくとも frame meta 全体の署名・manifest inclusion proof を仕様化する。openEHR 的には commit time は version/contribution の一部として改ざん検知対象に近い扱いにするべきです。

6. **clinical_note の「順序保存・無加工」は正しいが、構造化境界が足りない**

   医師自由記述の器を追加する判断は妥当です。ただし `narrative` だけだと、原文、表示用整形、抽出済み構造、AI 要約が混ざりやすい。後から FHIR Composition / DocumentReference / DiagnosticReport へ写像すると破綻します。

   改善方向: `clinical_note.content` は `raw_text`, `sections[]`, `source_system`, `source_document_id`, `language`, `encoding`, `ingested_from` を分ける。抽出タグや要約は投影層に置く。

7. **active_conditions / active_medications と FHIR status の写像が危険域**

   FHIR Condition は `clinicalStatus` と `verificationStatus` が別軸です。`active` / `resolved` だけに畳むと、`unconfirmed`, `provisional`, `differential`, `entered-in-error`, `refuted` を active problem list に誤混入します。MedicationRequest も `active/completed/stopped/cancelled/entered-in-error/draft` と intent が絡みます。

   改善方向: MedBeads 独自 status と FHIR status を同一視しない。`record_status`、`clinical_lifecycle_status`、`verification_status`、`attestation_status` を分ける。active view は「表示ビュー」であり、FHIR 原 status は保持し、導出理由を `rule_id` 付きで残す。

8. **parents の意味を「臨床文脈上の親」に純化すると patient_root 導出が揺れる**

   現行実装は parents から patient_root を導出します。assessment が複数根拠 Bead を parents に持つ設計は自然ですが、複数 patient_root の混入、shared master 参照、AI assessment の根拠横断をどう扱うか未定義です。

   改善方向: `parents` を `context_of` / `based_on` / `subject_of` のように edge role 化するか、最低限 `parents` とは別に `subject_ref` / `patient_root` 導出規則を明記する。cross-patient parents は原則禁止、shared Bead は evidence/reference として扱う。

9. **API 語彙変更が互換名の整理に留まり、契約の意味変更が未定義**

   `antigens -> tags`, `get_links` は良い。ただし tags は投影由来なので、Bead JSON に含まれる field ではなくなります。現行 MCP の `create_bead` は antigens を自動算出してハッシュ対象にしています。ここは破壊的変更が大きい。

   改善方向: API レベルで `bead.content` と `projection.tags` を明確に別オブジェクトにする。`get_bead` は正本のみ、`get_bead_with_projection` または `retrieve` は manifest 付き投影を返す、という境界が必要です。

**中**

10. **sibling_link 降格は正しいが、relation の証拠粒度がまだ粗い**

   LOINC 同一コード・temporal 単独をトリガーから外す判断は妥当です。現行 APC は `clinical_correlation/warning` を一律に作るため、draft の方針は改善です。ただし `score_breakdown` だけでは「なぜ warning 以上を許可したか」の根拠が弱い。

   改善方向: `clinical_links` に `evidence_basis = cooccurrence | curated_knowledge | guideline | model_inference` を持たせる。warning 以上は curated source id 必須。共起リンクは常に `info` に固定する。

11. **projection_manifest の入力集合が不足している**

   `{code_version, dictionary_hash, config_hash, built_at}` だけでは、同じ manifest で何を再構築したかが分かりません。

   改善方向: `input_pod_set_hash`, `pod_watermarks`, `schema_version`, `projection_name`, `projector_version`, `patient_scope`, `started_at`, `completed_at`, `activation_state` を追加する。

12. **attestation が承認対象の状態をどう変えるか未定義**

   `attestation.parents=[承認対象]` は自然ですが、複数 attestation、reject 後の approve、承認者ロール、承認範囲が未定義です。

   改善方向: `verdict`, `scope`, `target_ids`, `attester_role`, `policy_version` を構造化し、state derivation では latest valid attestation を決定論的に選ぶ。

13. **v2.1 死文化の範囲はもっと広い**

   draft が挙げる `daily_summary`, `actor:`, EMR-CSV, 旧 relation 10種だけでは不足です。死文化するものは、Bead の `antigens` hash field、APC が sibling_link Bead を生成する設計、`bead_edges.edge_type='sibling'`、`sibling_pairs.sibling_link_id`、`search_antigens`、`get_sibling_links`、二次応答、APC watermark の意味、現行 retrieve の `include_siblings` です。

**軽微**

14. **draft の日付が環境日付より未来**

   現在の実行環境は 2026-07-10 ですが、draft は 2026-07-11 作成・ユーザー決定 2026-07-11 と書かれています。レビュー記録として残すなら日付を実際の決定日に合わせるべきです。

**§7 Open Questions への回答**

1. `amends/retracts` はハッシュ対象フィールドにしてよい。ただし「訂正イベント型」を併用すべき。フィールドだけで済ませる案は NO。
2. projection manifest + 決定論は研究再現には十分。臨床監査には `projection_run_id` と retrieve provenance、activation 履歴が必要。リンク full snapshot は必須ではない。
3. 未承認 Bead は既定除外が妥当。明示指定時のみ強いラベル付きで返す。
4. FHIR status との単純写像は危険。record status / clinical status / verification status / attestation status を分離する。
5. 死文化範囲は sibling/APC/antigen API と index schema まで広がる。仕様 v3.1 で明示的な deprecation list が必要。

**判定: 条件付き GO**

方向性は正しいです。特に antigens の hash 除外、リンク投影化、自由記述型追加は実装に進める価値があります。ただし実装前に、`amends/retracts` の event model、projection audit model、unattested 既定挙動、FHIR status 写像を仕様に固定してください。ここを曖昧にしたまま実装すると、後で再び破壊的変更になります。