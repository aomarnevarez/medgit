FROM python:3.12-slim

WORKDIR /app
COPY src ./src
ENV PYTHONPATH=/app/src \
    PYTHONUNBUFFERED=1 \
    MEDGIT_DATA=/data \
    PORT=8080
RUN mkdir -p /data && chown nobody:nogroup /data
EXPOSE 8080
USER nobody
CMD ["python", "-m", "medgit.server"]
