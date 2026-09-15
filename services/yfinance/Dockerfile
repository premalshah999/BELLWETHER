FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY service.py ./

RUN adduser --disabled-password --gecos "" --uid 10002 yfin
USER yfin

EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD python -c "import urllib.request;urllib.request.urlopen('http://127.0.0.1:8787/health',timeout=4)"

CMD ["python", "-u", "service.py"]
