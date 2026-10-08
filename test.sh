#!/bin/bash

# Configuration
SERVER_URL="http://localhost:9999/mcp"

echo "--- Testing Initialize ---"
curl -isS -X POST $SERVER_URL \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
      "protocolVersion": "2024-11-05",
      "capabilities": {},
      "clientInfo": {
        "name": "cli-tester",
        "version": "1.0.0"
      }
    }
  }'

echo -e "\n\n--- Testing List Tools ---"
curl -isS -X POST $SERVER_URL \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tools/list"
  }'

echo -e "\n\n--- Testing Error Handling ---"
curl -isS -X POST $SERVER_URL \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "non_existent_method"
  }'
