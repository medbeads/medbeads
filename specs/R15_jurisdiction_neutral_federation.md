# R15 法域中立の医療フェデレーションと regulatory profiles

ステータス: **将来実装（2026-07-15 設計記録）**

## 設計判断

MedBeads core は特定国の法律をデータモデルへ直接埋め込まない。不変 Bead、署名、患者同一性、
訂正履歴、clinical links、purpose、clearance、release manifest、監査 receipt という共通機構を提供し、
各法域の要件は versioned **regulatory profile** として外側から適用する。

法令名を保持するだけでは compliance を意味しない。profile は適用時点、法域、組織、目的、データ範囲、
承認主体、policy version、失効条件を宣言し、運用・契約・人による審査と組み合わせる。

## 混同してはならない三つの提供契約

1. **AI context retrieval**
   - `retrieve` が質問に関連する縦DAGとclinical linksをbounded/token-budgeted bundleとして返す。
   - truncationを明示するが、法的・業務的な完全カルテexportを保証しない。
2. **complete record export**
   - 適用される要求範囲のEHI/診療情報を、token truncationなしでFHIR/Bulk Data等により提供する。
   - AI向け`retrieve`をこの契約の代用にしない。
3. **secondary-use release**
   - 承認された研究・開発目的、最小必要データ、仮名化/匿名化、secure processing environment、
     output disclosure controlを伴うproject-scoped releaseとする。
   - 診療目的の患者横断取得と研究用releaseを同じnamespace・権限で扱わない。

## 閉鎖型 federated P2P

将来のP2Pは公開ネットワークではなく、認証・契約された医療機関、データ管理主体、研究環境、規制主体等が
役割付きnodeとして参加するpermissioned federationを想定する。peerは法的・運用上同格とは限らない。

- control plane: organization/tenant identity、公開鍵と失効、trust、patient identity assertion、consent、
  purpose、clearance、data permit、release承認、監査policyを管理する。
- data plane: mTLS等で認証した相手へ、承認されたBeadまたは計算jobだけを暗号化して転送する。
- raw Pod全体、患者root、Bead ID、問い合わせ・アクセスpatternをpublic DHTへ広告しない。
- Bead IDはobject identityでありnetwork locatorではない。外部探索には認可済みのopaque locator/manifestを使う。
- 受信側はcontent hash、組織署名、record status、release manifestを検証し、clinical linksを自組織の
  knowledge/policy generationで再構築する。外部のderived linkを無条件に正本化しない。

P2P transportにIPFS/libp2p等を将来利用する余地はあるが、MedBeads coreの必須条件にはしない。blockchainも
必須ではない。相互不信の組織が中央運営者なしで共有する順序付き監査logが本当に必要な場合に限り、
knowledge releaseやaudit receiptのcommitment等へ限定して別途評価し、医療情報本体をon-chainへ置かない。

## secondary-use release の同一性分離

- 元の患者root、FHIR identifier、施設内Bead IDはsource/approved transformation環境から外へ出さない。
- 仮名化・最小化後のcanonical contentは元Beadとは異なるrelease Beadであり、新しいIDを持つ。
- pseudonymはproject/release単位とし、法域profileが明示的に許可しないcross-project linkageを防ぐ。
- 対応表は分離されたidentity vaultで管理し、利用nodeへ提供しない。
- release manifestはsource watermark、変換contract、knowledge/rule generation、対象型・期間、除外、
  purpose、approver、signature、expiry/revocationを固定する。
- 訂正、撤回、同意・許可・鍵の失効をrelease利用者またはsecure environmentへ伝播できるようにする。

## regulatory profile の例

| Profile | 主な設計関心 | MedBeads側の接続点 |
|---|---|---|
| US Cures/ONC | EHI access/exchange、information blocking、standardized API | FHIR/USCDI export、要求範囲の完全性、privacy/security exception receipt |
| EU EHDS | 一次・二次利用、data permit、最小化、secure processing environment | data catalog、project release、pseudonymisation、output control、利用監査 |
| EU AI Act | AI risk/data governance、logging、transparency、人の監督 | context provenance、versioned manifest、truncation、再現可能な評価記録 |
| Japan secondary use | 認定主体を介した匿名・仮名加工医療情報の利活用 | 日本専用role/policy profile、identity vault、visiting/secure environment |

これは完全な法令マッピングではなく、実装をcoreから分離するためのrouting tableである。各profileの実装時に
法律、政省令、ガイドライン、契約、倫理審査、規制当局要件を専門家と再確認する。

## 実装順序

1. R13 FHIR server syncと、token truncationを行わない完全export contract
2. R14 patient identity/partition integrityとpermissioned organization trust
3. signed release manifest、purpose-bound authorization、expiry/revocation、audit receipt
4. project-scoped pseudonymisationとidentity vault、secure/visiting computation
5. 2施設間mTLS federationによる最小構成のfault/contamination試験
6. regulatory profile engineと法域別conformance test
7. 必要性が実証された場合のみprivate P2P transportや共有監査logを評価

## 論文との境界

現在のMedBeadsコンセプト論文には、日本固有の次世代医療基盤法または各国法の詳細な適合主張を追加しない。
本仕様は将来の実装・別論文・法域別deployment profileのための設計記録である。
