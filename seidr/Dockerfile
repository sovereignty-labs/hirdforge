FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

RUN useradd -m -u 1000 seidr

COPY --chown=seidr:seidr main.py .

USER seidr

EXPOSE 8082

CMD ["python", "main.py"]
