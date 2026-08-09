use std::sync::Arc;

use jsonwebtoken::DecodingKey;
use metap_crud::CrudService;
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;
use sqlx::PgPool;

#[derive(Clone)]
pub struct AppState {
    pub pool: PgPool,
    pub metadata: Arc<MetadataRegistry>,
    pub permissions: Arc<PermissionService>,
    pub crud: Arc<CrudService>,
    pub jwt_decoding_key: Arc<DecodingKey>,
}

impl AppState {
    pub fn new(
        pool: PgPool,
        metadata: Arc<MetadataRegistry>,
        permissions: Arc<PermissionService>,
        jwt_decoding_key: DecodingKey,
    ) -> Self {
        let crud =
            Arc::new(CrudService::new(pool.clone(), metadata.clone(), permissions.clone()));
        Self { pool, metadata, permissions, crud, jwt_decoding_key: Arc::new(jwt_decoding_key) }
    }
}
