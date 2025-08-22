CHANGE REPLICATION SOURCE TO
  MASTER_HOST='db-primary',
  MASTER_PORT=3306,
  MASTER_USER='replication',
  MASTER_PASSWORD='replication',
  MASTER_AUTO_POSITION=1,
  MASTER_SSL=1,
  MASTER_SSL_CA='/etc/mysql/certs/ca.crt',
  MASTER_SSL_CERT='/etc/mysql/certs/mysql.crt',
  MASTER_SSL_KEY='/etc/mysql/certs/mysql.key';

START REPLICA;
