#!/bin/bash
until ./mijia-exporter; do
    echo "Server 'mijia-exporter' crashed with exit code $?.  Respawning.." >&2
    sleep 1
done
