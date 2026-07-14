# R11 link_rule v2 と患者優先ローリング再投影

日付: 2026-07-14

## 目的

知識・ガイドライン・投影コードの変更で10万〜100万患者を一斉に同期再投影しない。新しい解釈世代を
目標として登録し、患者ごとに原子的に移行する。新規データのある患者はキューを待たず即時更新し、
バックグラウンド処理は最近の受診患者、長期未受診患者、死亡情報のある患者の順に進める。

## link_rule v2

- v2は `execution.frequency_threshold` と `execution.max_links_per_bead` を知識Bead内に保持する。
- `trigger.min_shared`、`score_model.weights.shared_tag`を実際のリンク選択・score_breakdownへ適用する。
- v1 Beadは不変の過去知識として引き続き読め、欠ける実行設定にはv1既定値を使う。
- curated ruleは `revision_label`、`effective_period`、`evidence_bead_ids`を保持できる。
- guideline根拠のruleは外部根拠Beadを最低1件要求し、投影時のevidenceにはrule Bead自身も必ず含める。
- clinical_linksの一意性にrule_versionを含め、同じ関係を複数ガイドラインが支持した場合も各主張・根拠を
  上書きせず保持する。
- CLI公開時はauthorを必須にする。Signatureは保存されるが、DID/JWSの暗号学的検証は別ユニットであり、
  現時点で署名済みと断定する根拠にはしない。

## ローリング世代

`projection_manifest`のactiveは全患者が同時に切り替わったことではなく「現在の目標知識世代」を示す。
各患者が実際に使う世代は`patient_projection_state.clinical_links_run_id`が正確に示す。

知識世代の変更時は以下を行う。

1. 閉じたknowledge Bead集合を検証する。
2. 新しいmanifestを目標世代としてactiveにする。
3. `patient_projection_state`の世代不一致を仮想queueとして扱う（一患者一queue行を作らない）。
4. 一患者ずつclinical_linksを全置換し、その患者のcheckpointとqueue削除を同一transactionでcommitする。

`patient_reprojection_queue`実表は投影失敗のattempt/errorだけを保持する。途中で別の知識世代が公開された
場合、active targetの切替だけで未処理患者は自動的に新しい世代の対象になる。全患者の古い仕事を
最後まで実行してから新世代へ進む必要はない。

## 優先度

`patient_activity`は臨床状態ではなく、Beadから再構築可能なスケジューリングhintである。

1. `last_encounter_at`（なければ`last_clinical_at`）が`inactive_after`以内
2. 受診歴なし、または既定3年間より古い
3. `deceased_hint=1`

死亡情報不明のlegacy患者は0として扱い、安全側に高い優先度を維持する。死亡hintは患者の診断・死亡判定
には使用しない。新規Beadの追記、Pod crash recovery、訂正状態更新はこの優先順位を使わず即時処理する。

## 運用

- `serve`: 既定25患者/30秒の小バッチ。患者間でwrite lockを解放する。
- `reproject -batch-size N`: 今回N患者だけ処理。
- `reproject -batch-size 0`: 世代を有効化してqueue登録だけ行う。
- `reproject -drain`: 優先順を保ったままqueueを空にする保守運用。
- `-inactive-after`: 既定3年を施設ポリシーとして変更可能。
- link projectorのcode versionとrecord_stateのalgorithm contract versionを分離する。通常デプロイや
  link変更で訂正状態の全患者再構築を起こさず、record_stateの意味を変更した時だけ専用版を上げる。

## 不変条件

- 患者内では旧リンク集合または新リンク集合のどちらかだけを観測し、混在しない。
- 通常Ingestは他患者を走査しない。
- queueとpatient_activityを失ってもPod、knowledge Bead、manifest、patient checkpointから再開可能。
- 高severityリンクはrule_versionと空でないevidence_bead_idsを必須とする既存SQL制約を維持する。
