#!/bin/bash
# MedBeads - 全サーバー停止スクリプト
# Usage: ./stop.sh

echo "🛑 MedBeads サーバー停止中..."

# 各ポートのプロセスを停止
echo "  - Core Server (Port 8080)..."
lsof -t -i :8080 | xargs kill -9 2>/dev/null || true

echo "  - AI API Server (Port 8000)..."
lsof -t -i :8000 | xargs kill -9 2>/dev/null || true

echo "  - UI Server (Port 5174)..."
lsof -t -i :5174 | xargs kill -9 2>/dev/null || true

echo ""
echo "✅ 全サーバーを停止しました"
