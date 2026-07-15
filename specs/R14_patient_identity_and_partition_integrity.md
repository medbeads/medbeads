# R14 患者同一性とパーティション整合性

ステータス: **将来実装（2026-07-15 設計記録）**

## 目的

他患者の Bead が通常の `parents` または `clinical_links` に混入し、生成 AI の患者文脈へ入る
cross-patient graph contamination を fail closed で防止する。同時に、多施設間で同一人物と判断した
患者 root 同士を、通常の臨床エッジとは分離した、署名・撤回可能な identity layer で扱う。

Pod は患者単位の物理パーティションであり、それだけで患者同一性の正しさを証明するものではない。
本仕様は、構造的な混入防止と、FHIR 由来の患者照合の両方を production invariant として追加する。

## 分離する二つの問題

1. **構造的混入**: 異なる `patient_root` の endpoint を通常の parent/link が結ぶ。
2. **意味的な患者誤同定**: source EHR/FHIR が別人の記録へ一貫して同じ Patient を付与する。

1 は MedBeads の検証・DB 制約で拒否できる。2 はグラフ構造だけでは検出できないため、FHIR の
namespaced identifier、MPI/照合 assurance、組織署名、監査・人手 adjudication が必要である。

## 不変条件

- patient-scoped Bead の ingest は `expected_patient_root` を受け取り、変換された subject と一致しなければ
  append/index 投影を行わず quarantine する。
- patient-scoped Bead の通常の `parents`、`amends`、`retracts` はすべて同一 `patient_root` に属する。
- 通常の `clinical_links` は `root(bead_a) = root(bead_b) = clinical_links.patient_root` を必須とする。
- patient Pod 外の endpoint は検索結果から黙って除外して続行せず、投影・検証時には制御エラーまたは
  quarantine とする。取得時の既存 fail-closed 境界は維持する。
- 多施設 patient root 間の対応は通常の `parents` / `clinical_links` に入れない。
  専用 `patient_identity_link` として identity/federation scope に保存する。
- `patient_identity_link` が承認されても root や Pod は統合しない。取得時に trust、consent、purpose、
  clearance が許可した範囲だけ federation context を構成する。

## patient_identity_link の最低フィールド

- 両施設の namespaced patient root / FHIR Patient logical identity
- 根拠となる FHIR `Patient.link` または `Person.link` の source snapshot ID
- issuer organization、approver、署名、assurance level、照合方式
- consent / purpose-of-use / clearance policy の参照
- `effective_from` / `effective_to`、status、created_at
- supersedes / retracts による訂正履歴

FHIR の `Patient.link` / `Person.link` は照合根拠の入力であり、それだけを無条件な横断取得許可として
扱わない。MedBeads 側で承認・署名された identity assertion に昇格して初めて利用可能とする。

## 実装項目

1. 現在の shared-parent 利用を互換性監査する。共有知識は patient parent から Evidence または専用の
   governed knowledge reference へ移行してから、patient parent の同一 root 制約を有効化する。
2. ingest API に `expected_patient_root` と source patient identity を導入し、不一致・未解決参照を
   quarantine receipt へ記録する。複数 root から `_shared.pod` へ黙ってフォールバックしない。
3. SQLite migration で `clinical_links` の INSERT/UPDATE に endpoint root 一致 trigger を追加する。
   application validation と DB invariant の二重防御にする。
4. `verify_integrity` を拡張し、Pod metadata、Bead edges、amends/retracts、clinical_links の root 整合性を
   検査する。違反は canonical fact を自動修復せず、報告・隔離・再投影候補化する。
5. `patient_identity_link` の Bead/投影 schema、署名検証、trust/consent/clearance 判定を実装する。
6. `retrieve` は既定で一患者 root を維持する。federated retrieval は明示 opt-in とし、使用した identity
   assertion、施設、policy、除外理由を非漏洩な監査情報として返す。

## ロールアウト順序

1. 既存データの read-only compatibility audit
2. quarantine/receipt と ingest validation
3. DB trigger と `verify_integrity` 拡張
4. 共有知識参照の移行後、通常 parent の同一 root 強制
5. `patient_identity_link` と federated retrieval
6. cross-patient fault injection、FHIR 患者取り違え、署名失効、identity link 撤回の統合テスト

R13 の FHIR server 同期を実装する際、本仕様を patient mapping と quarantine の必須境界として同時に
組み込む。既存データを監査せずに制約だけを有効化しない。
