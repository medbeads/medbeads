# MedBeads

MedBeadsは、コンテンツアドレス化された不変の **Bead** と、再構築可能な
**clinical links**からなる、生成AI向け医療情報基盤です。患者の縦断的記録を患者単位Podへ格納し、
明示的なリンクを決定論的に辿ることで、生成AIが必要とする連結コンテキストを準備します。

![MedBeadsコンセプト](docs/concept-image.jpeg)

> **公開配布プロファイル:** このリポジトリは研究用reference implementation／論文再現デモです。
> 合成データだけを含み、本番電子カルテ、医療機器、臨床利用可能なdeploymentではありません。
> 詳細は[R16](specs/R16_public_demo_and_production_boundary.md)を参照してください。

[English](README.md)

## Dockerクイックスタート

必要環境はDocker Desktop、またはDocker Engine＋Compose v2だけです。

```bash
git clone https://github.com/medbeads/medbeads.git
cd medbeads
docker compose up --build
```

起動後、**http://localhost:5174** を開きます。初回ビルドではGoコアのコンパイル、Pod検証、
`index.db`の再構築、clinical linksの投影、React UIのビルドを自動実行します。
APIキー、外部FHIRサーバ、Python環境、ローカルのGo／Node.jsは不要です。

公開ポートはlocalhostだけに限定しています。

- UI: http://127.0.0.1:5174
- REST API／MCP HTTP endpoint: http://127.0.0.1:8080

停止とデモコンテナの削除は次のコマンドです。

```bash
docker compose down
```

再度`docker compose up`を実行すると、イメージに格納された合成seedから起動します。
ソース変更後に再構築する場合は`docker compose up --build`を使用します。

## 確認できる内容

同梱データは、決定的に選択したSynthea合成患者10人、患者Bead 4,202件、
共有knowledge Pod 1件、投影済みclinical links 492件です。

1. UIで患者を選択します。
2. 縦方向のparent／時間DAGと、横方向の`clinical_links`を確認します。
3. viewer roleを変更し、security clearanceによる表示制御を確認します。
4. REST APIを確認します。

   ```bash
   curl http://127.0.0.1:8080/patients
   ```

5. 全Pod frameとBead hashを検証します。

   ```bash
   docker compose exec core medbeadsd verify -data /data
   ```

同じGoコアが`/mcp`でMCPを提供し、決定論的retrieveと上限付きclinical-link展開を
生成AIクライアントへ返せます。外部LLMは任意であり、paper-demoコンテナには含めません。

## 公開デモと本番開発の区別

公開デモは意図的に次の範囲へ限定します。

- 合成患者10人と固定されたBead corpus
- 新規患者登録と`create_bead`権限なし
- service tokenを持たない`viewer` MCP role
- localhost限定ポート
- FHIR credential、病院identity、秘密鍵、実患者データなし
- clearance表示確認用の、破棄可能な派生状態変更

本番開発では同じcoreの意味論を利用しますが、施設FHIR endpoint、患者identity mapping、
trust policy、credential、KMS/HSM、監視、backup、実データは、別管理のprivate deployment
overlayに置きます。R13/R14の完全性制御および運用・セキュリティ・性能・規制検証を満たすまでは、
MedBeadsをproduction-readyとは表記しません。

## 構成

```text
ブラウザ
  │
  ▼
Nginx / React UI :5174
  │  /api/core/*
  ▼
medbeadsd :8080 ── REST + MCP
  │
  ├── 患者単位Pod              不変の正本
  └── index.db                 再構築可能な検索／リンク投影
```

`medbeadsd`はengine、REST API、MCP serverを統合した単一Goデーモンです。
Docker buildは開発機で作ったSQLiteを配布せず、commitされたPod正本からSQLiteを再構築します。

## Dockerを使わないローカル開発

必要環境はGo 1.25以上、C compiler、Node.js 24以上です。

```bash
# Core
CGO_ENABLED=1 go build -tags sqlite_fts5 -o medbeadsd ./cmd/medbeadsd
./medbeadsd reindex -data ./demo_data
./medbeadsd reproject -data ./demo_data -code-version local-demo -record-state -drain
./medbeadsd serve -data ./demo_data -role viewer -http 127.0.0.1:8080 \
  -projection-code-version local-demo
```

別のterminalでUIを起動します。

```bash
cd ui
cp .env.example .env.local
npm ci
npm run dev
```

`bench/`配下のPythonコードは再現可能な取込・benchmark用であり、v3の常駐serviceではありません。

## 完全性と派生リンク

- Bead IDはcanonical contentのSHA-256 digestです。
- Beadは患者単位Podへ追記され、上書きされません。
- `index.db`は派生状態で、Podから再構築できます。
- `clinical_links`はversion付きruleとprojection codeから導出し、患者単位で更新できます。
- retrieveはrecord status、患者partition、security clearance、depth／item／token上限を適用します。

設計と実装判断は[`specs/`](specs/)と[`docs/decisions.md`](docs/decisions.md)に記録しています。

## テスト

```bash
CGO_ENABLED=1 go test -tags sqlite_fts5 ./... -race
cd ui && npm test && npm run build
```

## 引用

研究で使用する場合は論文（[arXiv:2602.01086](https://arxiv.org/abs/2602.01086)）を引用してください。

```bibtex
@article{nakajima2026medbeads,
  title={MedBeads: An Agent-Native, Immutable Data Substrate for Trustworthy Medical AI},
  author={Nakajima, Takahito},
  journal={arXiv preprint arXiv:2602.01086},
  year={2026},
  doi={10.48550/arXiv.2602.01086},
  url={https://arxiv.org/abs/2602.01086}
}
```

`CITATION.cff`はGitHubの「Cite this repository」に対応しています。

## ライセンスとデータ

[Apache License 2.0](LICENSE)の下で公開します。帰属表示は[`NOTICE`](NOTICE)を参照してください。
同梱患者データはすべて[Synthea](https://synthetichealth.github.io/synthea/)による合成データで、
実際のPHIは含まれません。
