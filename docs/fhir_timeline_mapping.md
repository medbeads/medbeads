# FHIRデータ取得・表示項目対応表

このドキュメントは、FHIRリソースがMedBeadsシステムにインポートされ、タイムラインUIにどのように表示されるかの対応関係を示しています。

## 概要

- **データ取得元**: `medbeads/scripts/sample_patient.json`
- **インポート処理**: `medbeads/scripts/import_fhir.py`
- **表示処理**: `medbeads/ui/src/lib/api.ts` (`mapBeadToTimelineItem`関数)
- **調査日**: 2026-01-27

## 📊 表示可能な項目（タイムラインに表示される）

| FHIR Resource | 取得件数 | MedBeads Type | Timeline表示 | 備考 |
|:-------------|:--------|:------------|:-----------|:-----|
| **Patient** | 1 | patient_registration | ✅ Encounter | 患者登録として表示 |
| **Encounter** | 41 | fhir_encounter | ✅ Encounter | 外来・入院等の受診記録 |
| **Condition** | 45 | fhir_condition | ✅ Condition | 診断された病名・状態 |
| **Observation** | 241 | fhir_observation | ✅ Observation | 検査結果、バイタルサイン |
| **MedicationRequest** | 28 | fhir_medicationrequest | ✅ Medication | 処方薬情報 |
| **DiagnosticReport** | 81 | fhir_diagnosticreport | ✅ Diagnostic Report | 検査レポート |
| **DocumentReference** | 41 | fhir_documentreference | ✅ Diagnostic Report | 臨床ノート・退院サマリー |
| **Procedure** | 133 | fhir_procedure | ✅ Procedure | 手術・処置記録 |
| **Immunization** | 13 | fhir_immunization | ✅ Immunization | 予防接種記録 |
| **ImagingStudy** | 4 | fhir_imagingstudy | ✅ Imaging Study | 画像検査メタデータ |

## 🚫 表示されない項目（Beadとして保存はされるが、タイムラインには非表示）

| FHIR Resource | 取得件数 | MedBeads Type | Timeline表示 | 理由 |
|:-------------|:--------|:------------|:-----------|:-----|
| **Claim** | 69 | fhir_claim | ❌ 非表示 | 保険請求情報（管理用） |
| **ExplanationOfBenefit** | 69 | fhir_explanationofbenefit | ❌ 非表示 | 給付説明（管理用） |
| **SupplyDelivery** | 74 | fhir_supplydelivery | ❌ 非表示 | 物品配送記録（管理用） |
| **Device** | 13 | fhir_device | ❌ 非表示 | 使用デバイス情報 |
| **CarePlan** | 4 | fhir_careplan | ❌ 非表示 | ケアプラン |
| **CareTeam** | 4 | fhir_careteam | ❌ 非表示 | 医療チーム情報 |
| **MedicationAdministration** | 5 | fhir_medicationadministration | ❌ 非表示 | 投薬実施記録 |
| **Medication** | 5 | fhir_medication | ❌ 非表示 | 薬剤マスタ情報 |
| **Provenance** | 1 | fhir_provenance | ❌ 非表示 | データ来歴情報 |

## 制限の詳細

### 実装箇所
- **ファイル**: `medbeads/ui/src/lib/api.ts`
- **関数**: `mapBeadToTimelineItem` (85-302行)

### 処理フロー

1. **FHIRデータの取得**: `import_fhir.py`がFHIRバンドルからすべてのリソースを読み込み
2. **Beadへの変換**: 各FHIRリソースを`fhir_[resourcetype]`形式のBeadとして保存
3. **タイムライン表示**: `api.ts`が特定のタイプのみを選択的に表示

### 表示可能なタイプ（ハードコード）

```typescript
// 標準タイプ
- patient_registration
- encounter
- medication
- observation
- condition
- diagnostic_report

// FHIRタイプ
- fhir_encounter
- fhir_medicationrequest
- fhir_observation
- fhir_condition
- fhir_diagnosticreport
- fhir_documentreference
- fhir_procedure
- fhir_immunization
- fhir_imagingstudy
```

### 統計サマリー

- **総FHIRリソースタイプ**: 19種類
- **タイムライン表示可能**: 10種類（52.6%）
- **非表示**: 9種類（47.4%）
- **総データ件数**: 876件
- **表示可能データ件数**: 588件（67.1%）
- **非表示データ件数**: 288件（32.9%）

## 結論

MedBeadsシステムは、FHIRデータを完全にインポートし内部的に保存しますが、タイムラインUIでは臨床的に重要な情報（診断、処方、検査結果等）のみを選択的に表示する設計となっています。管理用・請求関連のデータは意図的に非表示となっています。

## 関連ドキュメント

- [mapping.md](./mapping.md) - FHIR to MedBeadsの基本マッピング仕様