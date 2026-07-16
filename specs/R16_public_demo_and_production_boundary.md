# R16 公開論文デモと本番開発の境界

ステータス: **設計決定（2026-07-15）**

## 目的

公開 GitHub 版の目的は、論文読者が MedBeads の主要概念を短時間で再現し、Bead、
`clinical_links`、患者単位 Pod、リンク展開、完全性検証を観察できることである。
公開版を電子カルテの本番配布物、医療機器、または本番運用可能性の証明として扱わない。

本番開発は同じ MedBeads core を利用するが、実データ、施設設定、FHIR 接続、秘密鍵、
identity mapping、監視・バックアップ等を公開論文デモから分離した deployment overlay で行う。

## 二つのプロファイル

| 項目 | Public paper demo（GitHub） | Production development（施設開発） |
|---|---|---|
| 目的 | 論文の概念再現・動作確認 | 実運用要件の実装・統合・検証 |
| データ | Synthea 等の合成10症例のみ | 施設が管理する隔離済み試験データ／実データ |
| 患者登録 | 提供しない | FHIR/MPI/施設ワークフロー経由で将来実装 |
| Bead 追記 | 無効。既存10患者とBead集合を固定 | 認証・認可・署名・quarantineを通る取込経路 |
| 権限 | `viewer`、system取込権限・service tokenなし | purpose/clearance、service identity、監査付き |
| ネットワーク | localhost のみ | mTLS、network policy、reverse proxy等を施設管理 |
| FHIR | 同梱fixtureを事前変換したデータ | R13/R14に従うserver sync・identity validation |
| 鍵・署名 | 実在組織の秘密鍵を持たない | KMS/HSM、鍵失効、trust policy、組織運用 |
| 永続化 | reset可能なデモ用volume | 暗号化、backup、restore、retentionを施設管理 |
| サポート範囲 | 小規模な再現性 | 性能、可用性、障害復旧、規制profileを別途検証 |

## 公開物の不変条件

1. 公開データは合成データだけとし、実患者情報、対応表、施設内部識別子をcommitしない。
2. デモの既定起動は`viewer`で、患者作成・`create_bead`・system roleを外部へ公開しない。
3. ホスト公開ポートは既定で`127.0.0.1`へbindし、LAN/Internetへ意図せず公開しない。
4. API key、FHIR credential、service token、private keyを要求・同梱しない。
5. デモ用の署名例が必要な場合は、実在施設と無関係な明示的test identityだけを使う。
6. `demo_data`は再生成手順と由来を固定し、患者数10、期待Bead/link数、検証結果を記録する。
7. デモだけ検証を迂回するcore分岐を作らない。安全条件とhash/link意味論は本番開発と共通にする。
8. READMEとUIに「research reference implementation / synthetic data / not for clinical use」を明示する。

デモUI内のsecurity clearance操作は、合成データ上で派生SQLite状態だけを変更する教育用機能として許容する。
Pod/Beadの追加ではなく、コンテナを削除すれば初期状態へ戻る。本番の認証済みpolicy変更経路を代替しない。

## Docker配布規約

公開リポジトリの既定Docker構成は **paper demo** だけを起動する。サービス名・image label・volume名にも
`demo`を含め、`viewer`、localhost bind、合成seedを明示する。読者向けの目標操作は次の3段階とする。

```bash
git clone https://github.com/medbeads/medbeads.git
cd medbeads
docker compose up --build
```

本番用`compose.production.yaml`を公開版の延長として用意しない。本番deploymentはprivate repositoryまたは
施設管理領域に置き、公開coreのrelease tagまたはimage digestをpinして利用する。公開デモの環境変数を
本番値に差し替えるだけで本番化できる、という誤った運用経路を作らない。

## リポジトリとリリース

- `main`: 公開可能で、CIが通り、論文デモの再現手順が成立する状態を保つ。
- feature branch / pull request: 本番機能を含む開発単位。完成前の主張をREADMEの実装済み機能にしない。
- paper release tag: 論文本文と対応するcode/data/image digestを固定する。
- private deployment overlay: 施設FHIR endpoint、identity mapping、trust policy、秘密情報、運用設定を保持する。

共通coreを公開版と本番版でforkしない。forkするとhash規約、投影、retrieveの挙動が乖離し、論文の再現性と
本番検証の両方が失われるためである。分離対象はデータ・権限・接続・運用設定・release claimであり、
MedBeadsの意味論そのものではない。

## 完成条件

### Public paper demo

- Dockerだけで起動でき、外部API keyを要求しない。
- 合成10患者が表示され、Beadと`clinical_links`を確認できる。
- リンク展開した生成AI向けcontext bundleを取得できる。
- Pod/Beadの完全性検証が成功する。
- 患者新規登録機能が公開されていない。
- reset後も同じ件数とdigestを再現できる。

### Production development

R13 FHIR同期、R14患者同一性・partition integrity、組織署名、KMS/HSM、監査、backup/restore、
負荷・障害試験、施設別security/regulatory reviewを満たすまでは「production ready」と表記しない。
