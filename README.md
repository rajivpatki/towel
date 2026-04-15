## To run

```bash
docker run -p 3000:3000 -p 8000:8000 -v insert_email_id_here:/data towel
```


## For developers

To build:

```bash
docker build -t towel .
docker run -P 3000:3000 -p 8000:8000 -v insert_email_id_here:/data towel
```