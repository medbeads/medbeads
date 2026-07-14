# R13: FHIRサーバ同期・整合性検証・Bead変換案

## 1. 現状

`bench/bench/ingest`にはSyntheaの患者Bundleファイルを決定的にBeadへ変換する実装がある。
患者root、時刻順、Encounter親、MedicationReference解決、DocumentReference本文変換、同一入力から
同一Bead IDを得るテストまで存在する。一方、FHIRサーバへの接続、継続的な差分同期、
`meta.versionId`履歴、削除、FHIR Provenance署名、同期checkpointはまだ実装されていない。

実データ連携ではbench importerを直接常駐化せず、次の責務を分離する。

```text
FHIR capability確認
  -> 差分取得・pagination
  -> source検証／quarantine
  -> 不変FHIR snapshot Bead
  -> 参照解決・臨床Bead変換
  -> signature_attestation
  -> 患者Pod追記・患者だけ即時project
  -> page checkpoint
```

## 2. 同期方式

### 初回

1. サーバがFHIR Bulk Data `$export`を提供する場合は患者・リソース型を限定したNDJSON exportを優先。
2. 非対応ならPatient compartment/resource type検索をページングする。
3. `Bundle.link[relation=next]`のURLをそのまま追い、独自にpage番号を生成しない。

### 差分

1. `_history?_since=`が利用可能なら、create/update/deleteとversionを取得できるため最優先。
2. Subscriptionが利用できる場合も、通知はtriggerとして使い、resource本体をversion指定で再取得する。
3. fallbackは`_lastUpdated=ge...`検索。サーバの時刻精度・ページング中更新を考慮し、checkpointより
   数分前からoverlap取得して、source keyとhashで重複排除する。
4. 大規模FHIRサーバではBulk Dataの`_since`を定期差分に利用できる。

checkpointは`source_server_id + tenant_id + resource_type`単位とし、page全件のBead取り込み成功後にだけ
進める。crash時は前pageを再取得するが、content addressによるidempotent ingestで重複しない。

## 3. Source identity

FHIRの`Resource.id`はサーバ内のlogical IDであり、病院間で一意ではない。最低限、次をhash対象contentへ
保存する。

```json
{
  "source": {
    "tenant_id": "tenant:a",
    "organization_id": "org:hospital-a",
    "fhir_server_id": "fhir:hospital-a-prod",
    "resource_type": "Observation",
    "logical_id": "123",
    "version_id": "7",
    "last_updated": "2026-07-14T09:00:00+09:00",
    "source_digest": "sha256 of canonical source resource",
    "mapping_version": "fhir-r4-to-medbeads-v1"
  }
}
```

一意キーは `(fhir_server_id, resource_type, logical_id, version_id)`。同じversionなのにsource digestが
異なる場合は上書きせずquarantineする。サーバ復元・不正変更・実装不良の可能性がある。

## 4. 二段階Bead

FHIR原文と臨床利用形を混ぜない。

1. `fhir_resource_snapshot`: サーバから取得したresourceを変更せず保持するsource Bead。
2. `fhir_observation`等: snapshotをparentに持つ、mapping version付きの臨床Bead。

現在のfile importerはMedicationReferenceのcodeをMedicationRequestへinline追加し、DocumentReferenceの
base64を本文へ変換する。これは検索には有用だがsource原文ではない。二段階化により、原文、参照解決、
本文抽出、将来のmapping修正を区別できる。大きなDocumentReference/Binary/DICOMはEvidenceへ置き、
hashとMIME typeを保持してFTSへbase64を入れない。

mapping ruleが変わったときはsource snapshotを再取得せず臨床Bead／projectionだけを再生成できる。

## 5. FHIR versionと訂正

- 同じlogical resourceの新versionは新しいsnapshot Beadとし、直前versionを`amends`で参照する。
- FHIR delete/history tombstoneは削除イベントBeadとして残し、最後のversionをretract対象にする。
- `entered-in-error`、Conditionの`clinicalStatus`と`verificationStatus`、MedicationRequestの`status`と
  `intent`は別軸のまま保持し、MedBeadsのrecord statusへ単純変換しない。
- 過去versionを捨てない。現在表示はrecord_state projectionで決める。

## 6. 整合性gate

source snapshotは証拠として保存可能だが、次を満たさないresourceを臨床投影へ入れずquarantineする。

- CapabilityStatementとFHIR version/profileが対応範囲内。
- `resourceType`, `id`, `meta.versionId`, `meta.lastUpdated`のshapeが正しい。
- subject/patient参照が一人に決まり、Encounterとresourceが同一患者。
- 参照先のresource typeが期待型で、別tenant・別患者へ越境しない。
- 必須code/status/date、UCUM/LOINC/SNOMED/RxNorm等のsystem/code組が解釈可能。
- 同一source keyのdigest不一致がない。
- 未解決参照はPatient rootへ黙ってfallbackせず、retry可能な`unresolved_reference`として記録する。
- 破損attachment、base64、外部URL、サイズ上限違反を隔離する。

warning/drop/quarantine件数を同期receiptへ記録し、サイレント破棄を禁止する。

## 7. Provenanceと署名

- FHIR `Provenance.target`はversion-specific referenceを優先してsource snapshotへ対応付ける。
- `Provenance.signature`を病院のtrust anchorで検証できた場合、`purpose=clinical_origin`の
  `signature_attestation`へ対応付ける。
- FHIR側に検証可能な署名がない場合、connectorは`purpose=fhir_import`で「取得・変換した事実」だけを
  署名する。医師本人がデジタル署名したとは扱わない。
- `Provenance.agent.who/onBehalfOf`をactor/organizationへ写像し、表示名だけで認証しない。
- AuditEventはアクセス監査、Provenanceはresource生成・変更由来として分ける。

## 8. 病院間クラウド

- tenantとorganizationを分離し、FHIR serverごとにsource namespaceとtrust keyを持つ。
- 病院Aが病院Bの署名を検証できても、自動的に閲覧権限を与えない。clearance、患者同意、利用目的を別途判定。
- `Patient/{id}`だけで患者を統合しない。病院別patient rootを維持し、明示承認されたpatient identity link
  Beadで統合する。
- clinical_linksはtenant/trust policy/knowledge release世代ごとに再構築可能とする。

## 9. 実装順序

1. FHIR source schemaとversion mapping table、quarantine table。
2. CapabilityStatement確認、OAuth2/mTLS secret provider、ページングclient。
3. `_history?_since`同期とoverlap/idempotency checkpoint。
4. source snapshot Beadと既存converterの二段階化。
5. Provenance取得・signature_attestation連携。
6. delete/amends/retractionと未解決参照retry。
7. Bulk Data bootstrap、Subscription trigger。
8. 10万・100万患者の同期／再開／障害注入試験。

## 10. 参照規格

- HL7 FHIR R4 REST/history: https://hl7.org/fhir/R4/http.html
- HL7 FHIR R4 search (`_lastUpdated`, `_include`, paging): https://hl7.org/fhir/R4/search.html
- HL7 FHIR R4 Provenance/signature: https://hl7.org/fhir/R4/provenance.html
- HL7 FHIR Bulk Data `$export`/`_since`: https://hl7.org/fhir/uv/bulkdata/OperationDefinition-export.html
