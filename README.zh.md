# MedBeads

MedBeads 是一个用于医疗 AI 的 **不可变 (Immutable)、原生代理 (Agent-Native) 数据基础设施**。

[English](README.md) | [日本語](README.ja.md) | [中文](README.zh.md)

## 系统架构

```mermaid
graph TD
    User((User))
    
    subgraph Frontend
        UI["React UI (Vite)<br/>Port: 5174"]
    end
    
    subgraph Backend
        Core["Go Core Server<br/>Port: 8080"]
        API["Python AI API<br/>Port: 8000"]
    end
    
    subgraph Storage
        Objects["Object Storage<br/>(CAS)"]
        SQL["Metadata DB<br/>(SQLite)"]
    end
    
    subgraph External
        Gemini[Gemini AI API]
    end

    User -->|Browser| UI
    UI -->|Data/Search| Core
    UI -->|AI Analysis| API
    
    API -->|Get Context| Core
    API -->|Generate| Gemini
    
    Core -->|Read/Write| Objects
    Core -->|Read/Write| SQL
```

### 目录结构

```
medbeads/
├── core/                    # Go 后端服务器
│   ├── main.go              # 入口点
│   ├── medbeads_data/       # 数据存储 (Docker 中挂载)
│   └── Dockerfile           # Core Dockerfile
│
├── api/                     # Python AI API 服务器
│   ├── main.py              # FastAPI 入口点
│   ├── ai.py                # Gemini AI 逻辑
│   └── Dockerfile           # API Dockerfile
│
├── ui/                      # React 前端
│   ├── src/                 # 源代码
│   └── Dockerfile           # UI Dockerfile
│
├── FHIR_sample/             # 样本数据 (Synthea)
├── docker-compose.yml       # Docker 配置文件
└── start.sh                 # 本地辅助启动脚本
```

## 配置 (Configuration)

要使用 AI 功能，您需要配置 Google Gemini API 密钥。

1. 复制示例环境变量文件:
   ```bash
   cp api/.env.example api/.env
   ```
2. 编辑 `api/.env` 并设置您的 API 密钥:
   ```
   GEMINI_API_KEY=your_actual_api_key_here
   ```

## 快速开始 (Docker)

运行 MedBeads 最简单的方法是使用 Docker。这将同时启动 Core、API 和 UI 服务。

### 前置条件
- Docker Engine
- Docker Compose

### 运行应用程序

1. 构建并启动容器:
   ```bash
   docker-compose up --build
   ```

2. **在浏览器中访问 UI:**
   
   👉 **http://localhost:5174**

3. 所有服务:
   - **UI (可视化):** [http://localhost:5174](http://localhost:5174)
   - **AI API:** [http://localhost:8000](http://localhost:8000)
   - **Core Engine (核心):** [http://localhost:8080](http://localhost:8080)

4. 停止应用程序:
   ```bash
   Ctrl+C
   ```

### 预加载的样本数据

此仓库包含 **3 个样本患者**，可以立即进行演示。要从 FHIR 样本中添加更多患者，请在 Docker 运行时执行以下命令:

```bash
# 添加更多患者（例如: 5 个额外）
uv run --with requests scripts/mass_ingest.py FHIR_sample --limit 5
```

## 本地开发 (手动)

如果您不想使用 Docker 而更喜欢单独运行服务，请按照以下步骤操作。

### 前置条件
- Go 1.21+
- Python 3.12+ (通过 `uv` 管理)
- Node.js 20+

### 一键启动 (推荐)
您可以使用辅助脚本来验证环境、摄入样本数据并一次性启动所有服务器:
```bash
./start.sh
```

### 手动步骤 (详细)

1. **启动 Core Engine (Go):**
   该服务负责管理数据存储和索引。
   ```bash
   cd core
   go run main.go
   # Server runs on localhost:8080
   ```

2. **摄入初始数据 (Python):**
   *(如果数据库为空，则必须执行此步骤)*
   将 FHIR 样本数据转换为 Beads 并发送到 Core Engine。
   请在 Core 运行时打开一个 **新终端** 执行:
   ```bash
   # 摄入 5 个样本患者数据
   uv run --with requests scripts/mass_ingest.py medbeads/FHIR_sample --limit 5
   ```

3. **启动 AI API (Python):**
   该服务提供 AI 分析功能。
   ```bash
   cd api
   uv run uvicorn main:app --host 0.0.0.0 --port 8000
   ```

4. **启动 UI (React):**
   前端可视化界面。
   ```bash
   cd ui
   npm install
   npm run dev
   # Access at http://localhost:5174
   ```

## 数据架构与摄入流程

1. **FHIR 源数据**
   - 位于 `medbeads/FHIR_sample/`。
   - 包含原始 FHIR JSON 文件。

2. **摄入流程 (Python)**
   - 运行 `python scripts/mass_ingest.py` (或通过 `uv run`)。
   - 脚本读取 JSON 文件，将其转换为 **Beads** (Merkle Graph Nodes)，并发送到 Core Server。

3. **存储 (Core Engine)**
   - **内容寻址存储 (CAS):** 原始数据作为不可变文件存储在 `medbeads/core/medbeads_data/objects/` 中。
   - **元数据索引 (SQLite):** 可搜索索引存储在 `medbeads/core/medbeads_data/metadata.db` 中。

## 填充种子数据

如果需要向仓库填充初始种子数据 (例如: 一半的样本) 并提交:

1. 启动 Core Server:
   ```bash
   cd core && go run main.go
   ```
2. 运行摄入脚本 (在另一个终端):
   ```bash
   uv run --with requests medbeads/scripts/mass_ingest.py medbeads/FHIR_sample --limit 5
   ```
3. (可选) 强制提交生成的数据:
   ```bash
   git add -f core/medbeads_data/metadata.db core/medbeads_data/objects/
   ```

## 📚 Citation（引用）

如果您在研究中使用 MedBeads，请引用我们的论文：

```bibtex
@article{medbeads2025,
  title={MedBeads: Immutable Agent-Native Data Infrastructure for Medical AI},
  author={Nakajima, Takahito},
  journal={medRxiv (审稿中)},
  year={2026},
  note={DOI: TBD}
}
```

## 🙏 Acknowledgement（致谢）

感谢 [Synthea](https://synthetichealth.github.io/synthea/) 提供本项目使用的合成 FHIR 患者数据。
