package postgres

import (
	"database/sql"
	"fmt"

	"github.com/chrislusf/seaweedfs/weed/filer"
	"github.com/chrislusf/seaweedfs/weed/filer/abstract_sql"
	"github.com/chrislusf/seaweedfs/weed/util"
	_ "github.com/lib/pq"
)

const (
	CONNECTION_URL_PATTERN = "host=%s port=%d sslmode=%s connect_timeout=30"
)

func init() {
	filer.Stores = append(filer.Stores, &PostgresStore{})
}

type PostgresStore struct {
	abstract_sql.AbstractSqlStore
}

func (store *PostgresStore) GetName() string {
	return "postgres"
}

func (store *PostgresStore) Initialize(configuration util.Configuration, prefix string) (err error) {
	return store.initialize(
		configuration.GetString(prefix+"username"),
		configuration.GetString(prefix+"password"),
		configuration.GetString(prefix+"hostname"),
		configuration.GetInt(prefix+"port"),
		configuration.GetString(prefix+"database"),
		configuration.GetString(prefix+"schema"),
		configuration.GetString(prefix+"sslmode"),
		configuration.GetInt(prefix+"connection_max_idle"),
		configuration.GetInt(prefix+"connection_max_open"),
	)
}

func (store *PostgresStore) initialize(user, password, hostname string, port int, database, schema, sslmode string, maxIdle, maxOpen int) (err error) {

	store.SupportBucketTable = false
	store.SqlGenerator = &SqlGenPostgres{
		CreateTableSqlTemplate: "",
		DropTableSqlTemplate:   "drop table %s",
	}

	sqlUrl := fmt.Sprintf(CONNECTION_URL_PATTERN, hostname, port, sslmode)
	if user != "" {
		sqlUrl += " user=" + user
	}
	if password != "" {
		sqlUrl += " password=" + password
	}
	if database != "" {
		sqlUrl += " dbname=" + database
	}
	if schema != "" {
		sqlUrl += " search_path=" + schema
	}
	var dbErr error
	store.DB, dbErr = sql.Open("postgres", sqlUrl)
	if dbErr != nil {
		store.DB.Close()
		store.DB = nil
		return fmt.Errorf("can not connect to %s error:%v", sqlUrl, err)
	}

	store.DB.SetMaxIdleConns(maxIdle)
	store.DB.SetMaxOpenConns(maxOpen)

	if err = store.DB.Ping(); err != nil {
		return fmt.Errorf("connect to %s error:%v", sqlUrl, err)
	}

	return nil
}
