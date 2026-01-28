# FHIR to MedBeads Mapping

MedBeadsはFHIR形式のデータを内部的な「Bead」構造に変換して扱います。
以下は、FHIRリソースタイプがMedBeadsのどのタイプにマッピングされ、UIでどのように表示されるかの対応表です。

## 基本構造

| FHIR Resource | MedBeads Type | UI表示 (Timeline) | 備考 |
| :--- | :--- | :--- | :--- |
| `Patient` | `patient_registration` | **Encounter** | 患者登録イベントとして扱われます |
| `Encounter` | `encounter` / `fhir_encounter` | **Encounter** | 外来・入院などの受診記録 |
| `Condition` | `condition` / `fhir_condition` | **Condition** | 診断された病名・状態 |
| `Observation` | `observation` / `fhir_observation` | **Observation** | 検査結果、バイタルサイン |
| `MedicationRequest` | `medication` / `fhir_medicationrequest` | **Medication** | 処方薬情報 |
| `DiagnosticReport` | `diagnostic_report` / `fhir_diagnosticreport` | **Diagnostic Report** | 検査レポート |
| `DocumentReference` | `fhir_documentreference` | **Clinical Note** | 臨床ノート、退院サマリー |
| `Procedure` | `fhir_procedure` | **Procedure** | 手術・処置 (今回追加) |
| `Immunization` | `fhir_immunization` | **Immunization** | 予防接種 (今回追加) |
| `ImagingStudy` | `fhir_imagingstudy` | **Imaging Study** | 画像検査メタデータ (今回追加) |

## 詳細マッピング

### 1. Patient -> patient_registration
*   **Date**: `birthDate` または取込日時
*   **Data Fields**:
    *   `name`: `name` (Given + Family)
    *   `gender`: `gender`
    *   `birthDate`: `birthDate`

### 2. Encounter
*   **Date**: `period.start`
*   **Data Fields**:
    *   `department`: `type[0].text`
    *   `encounter_type`: `class.code` (e.g., outpatient, emergency)
    *   `chief_complaint`: `type[0].text` または `reasonCode`

### 3. Condition
*   **Date**: `recordedDate`
*   **Data Fields**:
    *   `condition_name`: `code.text`
    *   `clinical_status`: `clinicalStatus.coding[0].code` (active, resolved, etc.)

### 4. MedicationRequest
*   **Date**: `authoredOn`
*   **Data Fields**:
    *   `medication_name`: `medicationCodeableConcept.text`
    *   `dosage`: `dosageInstruction[0].text`

### 5. Observation
*   **Date**: `effectiveDateTime`
*   **Data Fields**:
    *   `display_name`: `code.text`
    *   `value`: `valueQuantity.value` + `unit` または `valueString`
    *   `interpretation`: `interpretation.coding[0].code` (H -> abnormal, etc.)

### 6. DiagnosticReport & DocumentReference
*   **Date**: `effectiveDateTime` / `date`
*   **Data Fields**:
    *   `findings`: Base64エンコードされた `content.attachment.data` または `presentedForm.data` をデコードして表示
    *   **※自動整形**: "Chief Complaint", "Plan" などのヘッダーを検出し、Markdownで見やすく整形

### 7. Procedure (New)
*   **Date**: `performedDateTime` / `performedPeriod.start`
*   **Data Fields**:
    *   `procedure_name`: `code.text`
    *   `status`: `status`
    *   `outcome`: `outcome.text`

### 8. Immunization (New)
*   **Date**: `occurrenceDateTime`
*   **Data Fields**:
    *   `vaccine_name`: `vaccineCode.text`
    *   `route`: `route.text`

---

## フィルタリングされているリソース
以下のリソースはMedBeads内部には保存されていますが、現在のTimeline（`api.ts`）では**表示対象外**です。

*   `Claim` (保険請求)
*   `ExplanationOfBenefit` (給付説明)
*   `CarePlan` (ケアプラン)
*   `CareTeam` (医療チーム)
*   `Device` (使用デバイス)
*   `SupplyDelivery` (物品配送)
*   `Provenance` (データの来歴)
