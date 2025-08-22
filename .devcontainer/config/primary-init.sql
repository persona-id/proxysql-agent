-- Create additional users
CREATE USER 'replication'@'%' IDENTIFIED BY 'replication';
GRANT REPLICATION SLAVE ON *.* TO 'replication'@'%';

CREATE USER 'proxysql'@'%' IDENTIFIED BY 'proxysql';
GRANT ALL PRIVILEGES ON *.* TO 'proxysql'@'%' WITH GRANT OPTION;

CREATE USER 'test-user--primary'@'%' IDENTIFIED BY 'password';
GRANT ALL PRIVILEGES ON *.* TO 'test-user--primary'@'%' WITH GRANT OPTION;

CREATE USER 'test-user--replica'@'%' IDENTIFIED BY 'password';
GRANT ALL PRIVILEGES ON *.* TO 'test-user--replica'@'%' WITH GRANT OPTION;

-- Flush privileges to apply changes
FLUSH PRIVILEGES;
